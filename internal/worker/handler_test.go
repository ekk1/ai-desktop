package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandlerHealthAndEmptyStatus(t *testing.T) {
	t.Parallel()
	handler := NewHandler("v-test", NewManager("instance-test"))

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	var healthBody HealthResponse
	decodeTestJSON(t, health.Body.String(), &healthBody)
	if healthBody.Status != "ok" || healthBody.Version != "v-test" || healthBody.InstanceID != "instance-test" {
		t.Fatalf("health = %#v", healthBody)
	}

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/v1/process", nil))
	var statusBody StatusResponse
	decodeTestJSON(t, status.Body.String(), &statusBody)
	if statusBody.Run != nil {
		t.Fatalf("status run = %#v", statusBody.Run)
	}
}

func TestHandlerRejectsUnknownAndOversizedStartBodies(t *testing.T) {
	t.Parallel()
	handler := NewHandler("test", NewManager("instance-test"))
	unknown := `{"command":"echo ok","stop_grace_seconds":10,"log_buffer_bytes":65536,"readiness":{"kind":"none","timeout_seconds":60},"unknown":true}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/process/start", strings.NewReader(unknown)))
	assertAPIError(t, response, http.StatusBadRequest, "invalid_json")

	oversized := `{"command":"` + strings.Repeat("x", int(MaxRequestBytes)+1) + `"}`
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/process/start", strings.NewReader(oversized)))
	assertAPIError(t, response, http.StatusRequestEntityTooLarge, "request_too_large")
}

func TestHandlerProcessLifecycleAndPlainTextLogs(t *testing.T) {
	manager := NewManager("instance-test")
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	handler := NewHandler("test", manager)
	body := `{"command":"trap 'exit 0' TERM; echo marker; while :; do sleep 0.1; done","stop_grace_seconds":1,"log_buffer_bytes":65536,"readiness":{"kind":"none","timeout_seconds":60}}`
	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/api/v1/process/start", strings.NewReader(body)))
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d body = %s", start.Code, start.Body.String())
	}
	var run Run
	decodeTestJSON(t, start.Body.String(), &run)
	waitForLog(t, manager, run.RunID, "marker\n")

	logs := httptest.NewRecorder()
	handler.ServeHTTP(logs, httptest.NewRequest(http.MethodGet, "/api/v1/process/"+run.RunID+"/logs", nil))
	if logs.Code != http.StatusOK || logs.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("logs status=%d content-type=%q", logs.Code, logs.Header().Get("Content-Type"))
	}
	if logs.Body.String() != "marker\n" {
		t.Fatalf("logs = %q", logs.Body.String())
	}
	if logs.Header().Get("X-Log-Start-Offset") != "0" || logs.Header().Get("X-Log-End-Offset") != "7" {
		t.Fatalf("offset headers = %q %q", logs.Header().Get("X-Log-Start-Offset"), logs.Header().Get("X-Log-End-Offset"))
	}

	mismatch := httptest.NewRecorder()
	handler.ServeHTTP(mismatch, httptest.NewRequest(http.MethodPost, "/api/v1/process/stale/stop", nil))
	assertAPIError(t, mismatch, http.StatusConflict, "run_mismatch")

	stop := httptest.NewRecorder()
	handler.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/v1/process/"+run.RunID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("stop status = %d body = %s", stop.Code, stop.Body.String())
	}
}

func TestHandlerLogEventsIncludeSnapshotAndChunkOffsets(t *testing.T) {
	manager := NewManager("instance-test")
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	run, err := manager.Start(context.Background(), shellRequest("echo first; sleep 0.2; echo second; trap 'exit 0' TERM; while :; do sleep 0.1; done"))
	if err != nil {
		t.Fatal(err)
	}
	waitForLog(t, manager, run.RunID, "first\n")
	server := httptest.NewServer(NewHandler("test", manager))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/process/"+run.RunID+"/logs/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
	defer cancel()
	response, err := http.DefaultClient.Do(request.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	events := readTestSSEEvents(t, scanner, 2)
	if events[0].kind != "snapshot" || events[0].offset != 0 || events[0].data != "first\n" {
		t.Fatalf("snapshot = %#v", events[0])
	}
	if events[1].kind != "chunk" || events[1].offset != 6 || events[1].data != "second\n" {
		t.Fatalf("chunk = %#v", events[1])
	}
}

type testSSEEvent struct {
	kind   string
	offset int64
	data   string
}

func readTestSSEEvents(t *testing.T, scanner *bufio.Scanner, count int) []testSSEEvent {
	t.Helper()
	events := make([]testSSEEvent, 0, count)
	var kind, data string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			kind = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "":
			var payload struct {
				Offset int64  `json:"offset"`
				Data   []byte `json:"data"`
			}
			decodeTestJSON(t, data, &payload)
			events = append(events, testSSEEvent{kind: kind, offset: payload.Offset, data: string(payload.Data)})
			if len(events) == count {
				return events
			}
			kind, data = "", ""
		}
	}
	t.Fatalf("stream ended after %d events: %v", len(events), scanner.Err())
	return nil
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, status, response.Body.String())
	}
	var envelope ErrorEnvelope
	decodeTestJSON(t, response.Body.String(), &envelope)
	if envelope.Error.Code != code || envelope.Error.Message == "" {
		t.Fatalf("error = %#v", envelope.Error)
	}
}

func decodeTestJSON(t *testing.T, input string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(input), target); err != nil {
		t.Fatalf("decode %q: %v", input, err)
	}
}
