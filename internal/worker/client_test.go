package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLogEventSSEPreservesArbitraryBytes(t *testing.T) {
	want := []byte{0xff, 0xfe, 0xe4, 0xb8}
	var stream bytes.Buffer
	writeWorkerSSE(&stream, "chunk", 17, want)

	var data string
	for _, line := range strings.Split(stream.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	event, err := decodeLogEvent("chunk", data)
	if err != nil {
		t.Fatal(err)
	}
	if event.Offset != 17 || !bytes.Equal(event.Data, want) {
		t.Fatalf("event = offset %d data %x, want offset 17 data %x", event.Offset, event.Data, want)
	}
}

func TestClientLifecycleAgainstRealHandler(t *testing.T) {
	manager := NewManager("instance-test")
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	server := httptest.NewServer(NewHandler("v-test", manager))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTPClient: server.Client(), MaxResponseBytes: 1 << 20}

	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.InstanceID != "instance-test" || health.Version != "v-test" {
		t.Fatalf("health = %#v", health)
	}
	run, err := client.Start(context.Background(), shellRequest("trap 'exit 0' TERM; echo client-marker; while :; do sleep 0.1; done"))
	if err != nil {
		t.Fatal(err)
	}
	waitForLog(t, manager, run.RunID, "client-marker\n")
	status, err := client.Status(context.Background())
	if err != nil || status.Run == nil || status.Run.RunID != run.RunID {
		t.Fatalf("status = %#v, error = %v", status, err)
	}
	logs, err := client.Logs(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if logs.StartOffset != 0 || logs.EndOffset != 14 || string(logs.Data) != "client-marker\n" {
		t.Fatalf("logs = %#v", logs)
	}
	stopped, err := client.Stop(context.Background(), run.RunID)
	if err != nil || stopped.State != StateStopped {
		t.Fatalf("stopped = %#v, error = %v", stopped, err)
	}
}

func TestClientReturnsStructuredAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusConflict)
		_, _ = response.Write([]byte(`{"error":{"code":"slot_busy","message":"already running"}}`))
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTPClient: server.Client(), MaxResponseBytes: 1024}
	_, err := client.Status(context.Background())
	var apiErr *ClientError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict || apiErr.Code != "slot_busy" || apiErr.Message != "already running" {
		t.Fatalf("error = %#v", err)
	}
}

func TestClientRejectsOversizedResponsesAndRedirects(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(strings.Repeat("x", 33)))
		}))
		defer server.Close()
		client := Client{BaseURL: server.URL, HTTPClient: server.Client(), MaxResponseBytes: 32}
		if _, err := client.Status(context.Background()); !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("Status() error = %v", err)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(`{"status":"ok"}`))
		}))
		defer target.Close()
		redirect := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Location", target.URL)
			response.WriteHeader(http.StatusFound)
		}))
		defer redirect.Close()
		client := Client{BaseURL: redirect.URL, HTTPClient: redirect.Client(), MaxResponseBytes: 1024}
		_, err := client.Health(context.Background())
		var apiErr *ClientError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusFound {
			t.Fatalf("Health() error = %#v", err)
		}
	})
}

func TestClientSubscribeLogsParsesEventsAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		flusher := response.(http.Flusher)
		_, _ = fmt.Fprint(response, "event: snapshot\ndata: {\"offset\":4,\"data\":\"b2xkCg==\"}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(response, "event: chunk\ndata: {\"offset\":8,\"data\":\"bmV3Cg==\"}\n\n")
		flusher.Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTPClient: server.Client(), MaxResponseBytes: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	events, failures, err := client.SubscribeLogs(ctx, "run-one")
	if err != nil {
		t.Fatal(err)
	}
	first := receiveClientLogEvent(t, events)
	second := receiveClientLogEvent(t, events)
	if first.Kind != "snapshot" || first.Offset != 4 || string(first.Data) != "old\n" {
		t.Fatalf("first = %#v", first)
	}
	if second.Kind != "chunk" || second.Offset != 8 || string(second.Data) != "new\n" {
		t.Fatalf("second = %#v", second)
	}
	cancel()
	select {
	case failure := <-failures:
		if failure != nil && !errors.Is(failure, context.Canceled) {
			t.Fatalf("stream error = %v", failure)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
}

func TestClientSubscribeLogsAllowsBase64ExpansionWithinDecodedLimit(t *testing.T) {
	want := bytes.Repeat([]byte{0xff, 'x', 0x80}, 24<<10)
	payload, err := json.Marshal(struct {
		Offset int64  `json:"offset"`
		Data   []byte `json:"data"`
	}{Offset: 9, Data: want})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(response, "event: snapshot\ndata: %s\n\n", payload)
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTPClient: server.Client(), MaxResponseBytes: int64(len(want))}

	events, failures, err := client.SubscribeLogs(context.Background(), "run-one")
	if err != nil {
		t.Fatal(err)
	}
	event := receiveClientLogEvent(t, events)
	if event.Offset != 9 || !bytes.Equal(event.Data, want) {
		t.Fatalf("event = offset %d data length %d, want offset 9 data length %d", event.Offset, len(event.Data), len(want))
	}
	if failure, open := <-failures; open && failure != nil {
		t.Fatalf("stream error = %v", failure)
	}
}

func TestClientSubscribeLogsRejectsDecodedEventOverLimit(t *testing.T) {
	payload, err := json.Marshal(struct {
		Offset int64  `json:"offset"`
		Data   []byte `json:"data"`
	}{Offset: 0, Data: bytes.Repeat([]byte{'x'}, 33)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(response, "event: snapshot\ndata: %s\n\n", payload)
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTPClient: server.Client(), MaxResponseBytes: 32}

	events, failures, err := client.SubscribeLogs(context.Background(), "run-one")
	if err != nil {
		t.Fatal(err)
	}
	if event, open := <-events; open {
		t.Fatalf("oversized event was delivered: %#v", event)
	}
	if failure := <-failures; !errors.Is(failure, ErrResponseTooLarge) {
		t.Fatalf("stream error = %v, want ErrResponseTooLarge", failure)
	}
}

func TestClientSubscribeLogsRejectsMalformedEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = response.Write([]byte("event: chunk\ndata: not-json\n\n"))
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTPClient: server.Client(), MaxResponseBytes: 1024}
	events, failures, err := client.SubscribeLogs(context.Background(), "run-one")
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	if failure := <-failures; failure == nil || !strings.Contains(failure.Error(), "decode log event") {
		t.Fatalf("stream error = %v", failure)
	}
}

func receiveClientLogEvent(t *testing.T, events <-chan LogEvent) LogEvent {
	t.Helper()
	select {
	case event, open := <-events:
		if !open {
			t.Fatal("log event stream closed")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for log event")
		return LogEvent{}
	}
}
