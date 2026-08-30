package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecutorPostsExactBodyAndExtractsConfiguredJSONPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("X-Test") != "value" {
			t.Errorf("request = %s %#v", request.Method, request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"prompt":"hello"}` {
			t.Errorf("body = %s", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"choices":[{"text":"done"}]}`)
	}))
	defer server.Close()
	request := PreparedRequest{
		URL: server.URL, Method: http.MethodPost, Headers: map[string]string{"X-Test": "value"}, Body: []byte(`{"prompt":"hello"}`),
		ResponseMode: ResponseModeJSON, ResponseContentPath: "choices.0.text", ConnectTimeout: time.Second,
		TotalTimeout: time.Second, MaxResponseBytes: 1024,
	}
	result, err := (Executor{}).Execute(context.Background(), request, func(string) {})
	if err != nil || result.Content != "done" || result.StatusCode != http.StatusOK {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestExecutorReturnsBoundedHTTPStatusExcerpt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(writer, strings.Repeat("failure", 2000))
	}))
	defer server.Close()
	request := PreparedRequest{
		URL: server.URL, Method: http.MethodPost, Body: []byte(`{}`), ResponseMode: ResponseModeJSON,
		ResponseContentPath: "content", TotalTimeout: time.Second, MaxResponseBytes: 1 << 20,
	}
	result, err := (Executor{}).Execute(context.Background(), request, nil)
	if !errors.Is(err, ErrHTTPStatus) || result.StatusCode != http.StatusBadGateway {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(result.ResponseExcerpt) == 0 || len(result.ResponseExcerpt) > maxErrorExcerptBytes {
		t.Fatalf("excerpt length = %d", len(result.ResponseExcerpt))
	}
}

func TestExecutorRejectsResponseLimitAndInvalidPath(t *testing.T) {
	t.Run("limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, `{"content":"too large"}`)
		}))
		defer server.Close()
		request := PreparedRequest{
			URL: server.URL, Method: http.MethodPost, Body: []byte(`{}`), ResponseMode: ResponseModeJSON,
			ResponseContentPath: "content", TotalTimeout: time.Second, MaxResponseBytes: 5,
		}
		if _, err := (Executor{}).Execute(context.Background(), request, nil); !errors.Is(err, ErrResponseLimit) {
			t.Fatalf("limit error = %v", err)
		}
	})

	t.Run("path", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, `{"choices":[]}`)
		}))
		defer server.Close()
		request := PreparedRequest{
			URL: server.URL, Method: http.MethodPost, Body: []byte(`{}`), ResponseMode: ResponseModeJSON,
			ResponseContentPath: "choices.0.text", TotalTimeout: time.Second, MaxResponseBytes: 1024,
		}
		if _, err := (Executor{}).Execute(context.Background(), request, nil); !errors.Is(err, ErrResponsePath) {
			t.Fatalf("path error = %v", err)
		}
	})
}

func TestExecutorHonorsContextCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		select {
		case <-request.Context().Done():
		case <-releaseHandler:
		}
	}))
	defer server.Close()
	defer close(releaseHandler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (Executor{}).Execute(ctx, PreparedRequest{
			URL: server.URL, Method: http.MethodPost, Body: []byte(`{}`), ResponseMode: ResponseModeJSON,
			ResponseContentPath: "content", TotalTimeout: 10 * time.Second, MaxResponseBytes: 1024,
		}, nil)
		done <- err
	}()
	<-requestStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not stop after cancellation")
	}
}
