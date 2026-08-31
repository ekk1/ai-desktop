package sdcpp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

func TestVideoClientSubmitsAndReadsCompletedVideoJob(t *testing.T) {
	server := httptest.NewServer(videoJobHandler(t, `{"id":"job-v","kind":"vid_gen","status":"completed","queue_position":0,"poll_url":"https://attacker.invalid/do-not-use","result":{"output_format":"webm","mime_type":"video/webm","fps":16,"frame_count":33,"b64_json":"R0tYZg=="},"error":null}`))
	defer server.Close()
	provider := testVideoProvider(server.URL)

	submitted, err := (VideoClient{}).Submit(context.Background(), provider, []byte(`{"prompt":"cat"}`))
	if err != nil || submitted.Kind != "vid_gen" || submitted.PollURL != "/sdcpp/v1/jobs/job-v" {
		t.Fatal(submitted, err)
	}
	job, err := (VideoClient{}).Job(context.Background(), provider, "job-v")
	if err != nil || job.Result == nil || job.Result.FrameCount != 33 || job.Result.MIMEType != "video/webm" {
		t.Fatal(job, err)
	}
}

func TestVideoClientRejectsMalformedCompletedVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"id":"job-v","kind":"vid_gen","status":"completed","queue_position":0,"result":{"output_format":"webm","mime_type":"video/webm","fps":16,"frame_count":33,"b64_json":"%%%"},"error":null}`)
	}))
	defer server.Close()

	if _, err := (VideoClient{}).Job(context.Background(), testVideoProvider(server.URL), "job-v"); err == nil {
		t.Fatal("Job accepted malformed base64 video")
	}
}

func TestVideoClientAcceptsOfficialVideoFormatMIMEPairs(t *testing.T) {
	for outputFormat, mimeType := range map[string]string{
		"webm": "video/webm",
		"webp": "image/webp",
		"avi":  "video/x-msvideo",
	} {
		t.Run(outputFormat, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(response, `{"id":"job-v","kind":"vid_gen","status":"completed","queue_position":0,"result":{"output_format":"`+outputFormat+`","mime_type":"`+mimeType+`","fps":16,"frame_count":33,"b64_json":"R0tYZg=="},"error":null}`)
			}))
			defer server.Close()

			if _, err := (VideoClient{}).Job(context.Background(), testVideoProvider(server.URL), "job-v"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVideoClientRejectsUnexpectedJobSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, `{"id":"job-v","kind":"vid_gen","status":"queued","queue_position":0,"result":null,"error":null}`)
	}))
	defer server.Close()

	if _, err := (VideoClient{}).Job(context.Background(), testVideoProvider(server.URL), "job-v"); err == nil {
		t.Fatal("Job accepted HTTP 201")
	}
}

func TestVideoClientReturnsTypedJobAndCancelErrors(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(status)
				_, _ = io.WriteString(response, `{"error":"missing"}`)
			}))
			defer server.Close()

			_, err := (VideoClient{}).Job(context.Background(), testVideoProvider(server.URL), "job-v")
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != status || !strings.Contains(httpErr.Body, "missing") {
				t.Fatalf("error = %#v", err)
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(response, `{"error":"cannot_cancel_generating"}`)
	}))
	defer server.Close()
	err := (VideoClient{}).Cancel(context.Background(), testVideoProvider(server.URL), "job-v")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		t.Fatal(err)
	}
}

func TestVideoClientBoundsRequestResponseAndErrorBodies(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		called := false
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		defer server.Close()
		provider := testVideoProvider(server.URL)
		provider.MaxRequestBytes = 1

		_, err := (VideoClient{}).Submit(context.Background(), provider, []byte(`{}`))
		if !errors.Is(err, ErrRequestTooLarge) || called {
			t.Fatalf("error = %v, called = %t", err, called)
		}
	})
	t.Run("encoded response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(response, strings.Repeat("x", 1_048_581))
		}))
		defer server.Close()
		provider := testVideoProvider(server.URL)
		provider.MaxVideoBytes = 1

		_, err := (VideoClient{}).Job(context.Background(), provider, "job-v")
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(response, strings.Repeat("x", 16))
		}))
		defer server.Close()
		provider := testVideoProvider(server.URL)
		provider.MaxErrorBytes = 8

		err := (VideoClient{}).Cancel(context.Background(), provider, "job-v")
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || len(httpErr.Body) != 8 {
			t.Fatalf("error = %#v", err)
		}
	})
}

func TestVideoClientRejectsInvalidVideoJobs(t *testing.T) {
	tests := map[string]string{
		"invalid JSON":        `{`,
		"trailing JSON":       `{} {}`,
		"wrong ID":            `{"id":"other","kind":"vid_gen","status":"queued","queue_position":0,"result":null,"error":null}`,
		"wrong kind":          `{"id":"job-v","kind":"img_gen","status":"queued","queue_position":0,"result":null,"error":null}`,
		"unknown status":      `{"id":"job-v","kind":"vid_gen","status":"unknown","queue_position":0,"result":null,"error":null}`,
		"negative queue":      `{"id":"job-v","kind":"vid_gen","status":"queued","queue_position":-1,"result":null,"error":null}`,
		"active result":       `{"id":"job-v","kind":"vid_gen","status":"queued","queue_position":0,"result":{"output_format":"webm","mime_type":"video/webm","fps":16,"frame_count":33,"b64_json":"R0tYZg=="},"error":null}`,
		"completed no result": `{"id":"job-v","kind":"vid_gen","status":"completed","queue_position":0,"result":null,"error":null}`,
		"wrong MIME":          `{"id":"job-v","kind":"vid_gen","status":"completed","queue_position":0,"result":{"output_format":"webm","mime_type":"video/mp4","fps":16,"frame_count":33,"b64_json":"R0tYZg=="},"error":null}`,
		"zero FPS":            `{"id":"job-v","kind":"vid_gen","status":"completed","queue_position":0,"result":{"output_format":"webm","mime_type":"video/webm","fps":0,"frame_count":33,"b64_json":"R0tYZg=="},"error":null}`,
		"failed no error":     `{"id":"job-v","kind":"vid_gen","status":"failed","queue_position":0,"result":null,"error":null}`,
	}
	for name, responseBody := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(response, responseBody)
			}))
			defer server.Close()

			if _, err := (VideoClient{}).Job(context.Background(), testVideoProvider(server.URL), "job-v"); err == nil {
				t.Fatal("Job succeeded")
			}
		})
	}
}

func TestVideoClientUsesEscapedSameProviderJobPaths(t *testing.T) {
	requested := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requested <- request.Method + " " + request.URL.EscapedPath()
		if request.Method == http.MethodGet {
			_, _ = io.WriteString(response, `{"id":"job a/b","kind":"vid_gen","status":"queued","queue_position":0,"result":null,"error":null}`)
		}
	}))
	defer server.Close()
	provider := testVideoProvider(server.URL)
	jobID := "job a/b"

	if _, err := (VideoClient{}).Job(context.Background(), provider, jobID); err != nil {
		t.Fatal(err)
	}
	if err := (VideoClient{}).Cancel(context.Background(), provider, jobID); err != nil {
		t.Fatal(err)
	}
	if got := <-requested; got != "GET /sdcpp/v1/jobs/job%20a%2Fb" {
		t.Fatal(got)
	}
	if got := <-requested; got != "POST /sdcpp/v1/jobs/job%20a%2Fb/cancel" {
		t.Fatal(got)
	}
}

func TestVideoClientRejectsRedirectAndHonorsContextCancellation(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		redirected := false
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			http.Redirect(response, request, target.URL, http.StatusFound)
		}))
		defer server.Close()

		_, err := (VideoClient{}).Job(context.Background(), testVideoProvider(server.URL), "job-v")
		if !errors.Is(err, ErrRedirect) || redirected {
			t.Fatalf("error = %v, redirected = %t", err, redirected)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		started := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(started)
			<-request.Context().Done()
		}))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		finished := make(chan error, 1)
		go func() {
			_, err := (VideoClient{}).Job(ctx, testVideoProvider(server.URL), "job-v")
			finished <- err
		}()
		select {
		case <-started:
			cancel()
		case <-time.After(time.Second):
			t.Fatal("request did not start")
		}
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("request did not stop")
		}
	})
}

func videoJobHandler(t *testing.T, responseBody string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "POST /sdcpp/v1/vid_gen":
			body, err := io.ReadAll(request.Body)
			if err != nil || string(body) != `{"prompt":"cat"}` {
				t.Fatalf("submission body = %q, error = %v", body, err)
			}
			response.WriteHeader(http.StatusAccepted)
		case "GET /sdcpp/v1/jobs/job-v":
		default:
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		_, _ = io.WriteString(response, responseBody)
	})
}

func testVideoProvider(baseURL string) videoconfig.HTTPProvider {
	provider := videoconfig.Default().HTTPProviders[0]
	provider.BaseURL = baseURL
	provider.Headers = map[string]string{"X-Test": "provider-header"}
	return provider
}
