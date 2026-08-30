package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExecutorStreamsLlamaSSEAndIgnoresPing(t *testing.T) {
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		_, _ = io.WriteString(writer, ": ping\n\ndata: {\"content\":\"hel\",\"stop\":false}\n\ndata: {\"content\":\"lo\",\"stop\":true}\n\n")
		flusher.Flush()
		select {
		case <-request.Context().Done():
		case <-releaseHandler:
		}
	}))
	defer server.Close()
	defer close(releaseHandler)
	request := PreparedRequest{
		URL: server.URL, Method: http.MethodPost, Body: []byte(`{}`), ResponseMode: ResponseModeSSEJSON,
		StreamContentPath: "content", StreamDonePath: "stop", TotalTimeout: 3 * time.Second, MaxResponseBytes: 1024,
	}
	var chunks []string
	result, err := (Executor{}).Execute(context.Background(), request, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil || result.Content != "hello" || !reflect.DeepEqual(chunks, []string{"hel", "lo"}) {
		t.Fatalf("result = %#v, chunks = %#v, error = %v", result, chunks, err)
	}
}

func TestSSEJoinsMultipleDataLinesAndHandlesDone(t *testing.T) {
	input := "data: {\"content\":\"a\"}\ndata: \n\n" + "event: ignored\nid: 2\ndata: [DONE]\n\n"
	events, err := readSSE(strings.NewReader(input), 1024)
	if err != nil || len(events) != 2 || events[0] != "{\"content\":\"a\"}\n" || events[1] != "[DONE]" {
		t.Fatalf("events = %#v, error = %v", events, err)
	}
}

func TestExecutorStopsAtStandardDoneMarker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"delta\":\"a\"}\n\ndata: [DONE]\n\ndata: {\"delta\":\"ignored\"}\n\n")
	}))
	defer server.Close()
	result, err := (Executor{}).Execute(context.Background(), PreparedRequest{
		URL: server.URL, Method: http.MethodPost, Body: []byte(`{}`), ResponseMode: ResponseModeSSEJSON,
		StreamContentPath: "delta", TotalTimeout: time.Second, MaxResponseBytes: 1024,
	}, nil)
	if err != nil || result.Content != "a" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestSSEEnforcesAggregateAndLineLimits(t *testing.T) {
	if _, err := readSSE(strings.NewReader("data: "+strings.Repeat("x", 100)+"\n\n"), 20); err == nil {
		t.Fatal("readSSE accepted an oversized stream")
	}
	if _, err := readSSE(strings.NewReader("data: "+strings.Repeat("x", maxSSELineBytes+1)+"\n\n"), int64(maxSSELineBytes*2)); err == nil {
		t.Fatal("readSSE accepted an oversized event line")
	}
}
