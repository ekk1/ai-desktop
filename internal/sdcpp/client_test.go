package sdcpp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientReadsCapabilitiesAndSubmitsNativeImageJob(t *testing.T) {
	requested := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requested <- request.Method + " " + request.URL.Path
		if request.Header.Get("X-Test") != "provider-header" {
			t.Errorf("provider header = %q", request.Header.Get("X-Test"))
		}
		switch request.URL.Path {
		case "/sdcpp/v1/capabilities":
			_, _ = io.WriteString(response, `{
				"model":{"name":"model","stem":"model","path":"/models/model.gguf"},
				"current_mode":"img_gen","supported_modes":["img_gen"],
				"defaults_by_mode":{"img_gen":{"width":768},"future":{"new_field":true}},
				"output_formats_by_mode":{"img_gen":["png","jpeg"]},
				"features_by_mode":{"img_gen":{"init_image":true}},
				"samplers":["euler"],"schedulers":["discrete"],
				"loras":[{"name":"detail","path":"detail.safetensors"}],
				"upscalers":[{"name":"Lanczos"}],
				"limits":{"min_width":64,"max_width":2048,"min_height":64,"max_height":2048,"max_batch_count":4,"max_queue_size":8}
			}`)
		case "/sdcpp/v1/img_gen":
			if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("submission request = %s, headers=%v", request.Method, request.Header)
			}
			body, _ := io.ReadAll(request.Body)
			if string(body) != `{"prompt":"cat"}` {
				t.Errorf("submission body = %s", body)
			}
			response.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(response, `{"id":"job-a","kind":"img_gen","status":"queued","created":1775401200,"poll_url":"https://attacker.invalid/do-not-use"}`)
		default:
			t.Errorf("path = %s", request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	provider := testImageProvider(server.URL)

	capabilities, err := (Client{}).Capabilities(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Model.Name != "model" || capabilities.Samplers[0] != "euler" || capabilities.Limits.MaxQueueSize != 8 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	var future map[string]bool
	if err := json.Unmarshal(capabilities.DefaultsByMode["future"], &future); err != nil || !future["new_field"] {
		t.Fatalf("future metadata = %s, %v", capabilities.DefaultsByMode["future"], err)
	}
	submitted, err := (Client{}).Submit(context.Background(), provider, []byte(`{"prompt":"cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if submitted.ID != "job-a" || submitted.Kind != "img_gen" || submitted.Status != "queued" {
		t.Fatalf("submission = %#v", submitted)
	}
	if got := <-requested; got != "GET /sdcpp/v1/capabilities" {
		t.Fatal(got)
	}
	if got := <-requested; got != "POST /sdcpp/v1/img_gen" {
		t.Fatal(got)
	}
}

func TestClientRejectsInvalidSubmissionAndRedirect(t *testing.T) {
	t.Run("invalid submission", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(response, `{"id":"","kind":"vid_gen","status":"unknown"}`)
		}))
		defer server.Close()
		if _, err := (Client{}).Submit(context.Background(), testImageProvider(server.URL), []byte(`{}`)); err == nil {
			t.Fatal("Submit succeeded")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		redirected := false
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			http.Redirect(response, request, target.URL, http.StatusFound)
		}))
		defer server.Close()
		if _, err := (Client{}).Capabilities(context.Background(), testImageProvider(server.URL)); err == nil {
			t.Fatal("Capabilities followed redirect")
		}
		if redirected {
			t.Fatal("redirect target requested")
		}
	})
}

func testImageProvider(baseURL string) ImageProvider {
	provider := DefaultImageConfig().Providers[0]
	provider.BaseURL = baseURL
	provider.Headers = map[string]string{"X-Test": "provider-header"}
	return provider
}

func TestClientReadsCompletedJobAndCancelsByID(t *testing.T) {
	requested := make(chan string, 2)
	jobID := "job a/b"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requested <- request.Method + " " + request.URL.EscapedPath()
		if request.Method == http.MethodGet {
			_, _ = io.WriteString(response, `{"id":"job a/b","kind":"img_gen","status":"completed","created":1,"started":2,"completed":3,"queue_position":0,"result":{"output_format":"png","images":[{"index":0,"b64_json":"aW1hZ2U="}]},"error":null}`)
			return
		}
		_, _ = io.WriteString(response, `{"id":"job a/b","kind":"img_gen","status":"cancelled","queue_position":0,"result":null,"error":{"code":"cancelled","message":"cancelled"}}`)
	}))
	defer server.Close()
	provider := testImageProvider(server.URL)
	job, err := (Client{}).Job(context.Background(), provider, jobID)
	if err != nil || job.Result == nil || job.Result.Images[0].Index != 0 || job.Result.Images[0].B64JSON != "aW1hZ2U=" {
		t.Fatalf("job = %#v, error = %v", job, err)
	}
	if err := (Client{}).Cancel(context.Background(), provider, jobID); err != nil {
		t.Fatal(err)
	}
	if got := <-requested; got != "GET /sdcpp/v1/jobs/job%20a%2Fb" {
		t.Fatal(got)
	}
	if got := <-requested; got != "POST /sdcpp/v1/jobs/job%20a%2Fb/cancel" {
		t.Fatal(got)
	}
}

func TestClientReturnsTypedHTTPError(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusConflict, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(status)
				_, _ = io.WriteString(response, `{"error":"remote"}`)
			}))
			defer server.Close()
			_, err := (Client{}).Job(context.Background(), testImageProvider(server.URL), "job-a")
			var httpError *HTTPError
			if !errors.As(err, &httpError) || httpError.StatusCode != status || !strings.Contains(httpError.Body, "remote") {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"supported_modes":["`+strings.Repeat("x", 256)+`"]}`)
	}))
	defer server.Close()
	provider := testImageProvider(server.URL)
	provider.MaxResponseBytes = 32
	if _, err := (Client{}).Capabilities(context.Background(), provider); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientRejectsInvalidJSONAndJobShape(t *testing.T) {
	tests := map[string]string{
		"invalid JSON":        `{`,
		"trailing JSON":       `{} {}`,
		"wrong ID":            `{"id":"other","kind":"img_gen","status":"queued","queue_position":0}`,
		"wrong kind":          `{"id":"job-a","kind":"vid_gen","status":"queued","queue_position":0}`,
		"unknown state":       `{"id":"job-a","kind":"img_gen","status":"mystery","queue_position":0}`,
		"negative queue":      `{"id":"job-a","kind":"img_gen","status":"queued","queue_position":-1}`,
		"completed no result": `{"id":"job-a","kind":"img_gen","status":"completed","queue_position":0}`,
	}
	for name, responseBody := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(response, responseBody)
			}))
			defer server.Close()
			if _, err := (Client{}).Job(context.Background(), testImageProvider(server.URL), "job-a"); err == nil {
				t.Fatal("Job succeeded")
			}
		})
	}
}

func TestClientHonorsContextCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := (Client{}).Capabilities(ctx, testImageProvider(server.URL))
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
}

func TestClientBoundsErrorBodyTo4096Bytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(response, strings.Repeat("x", 8192))
	}))
	defer server.Close()
	_, err := (Client{}).Job(context.Background(), testImageProvider(server.URL), "job-a")
	var httpError *HTTPError
	if !errors.As(err, &httpError) || len(httpError.Body) != 4096 {
		t.Fatalf("error = %#v", err)
	}
}
