package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/videogen"
)

func TestWriteVideoAPIErrorStatusAndRedactionContract(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name string
		err  error
		want int
		code string
	}{
		{"domain not found", videogen.ErrBatchNotFound, http.StatusNotFound, "not_found"},
		{"active conflict", videogen.ErrActiveAttempt, http.StatusConflict, "active_attempt"},
		{"move conflict", videogen.ErrMoveBoundary, http.StatusConflict, "move_boundary"},
		{"validation", errors.New("video title is required"), http.StatusBadRequest, "invalid_video"},
		{"path failure", &os.PathError{Op: "open", Path: filepath.Join(root, "private", "batch.json"), Err: errors.New("disk failure")}, http.StatusInternalServerError, "storage_error"},
		{"executor shutdown", videogen.ErrCLIExecutorShutdown, http.StatusInternalServerError, "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeVideoAPIError(response, test.err)
			raw := append([]byte(nil), response.Body.Bytes()...)
			var envelope errorEnvelope
			if err := json.Unmarshal(raw, &envelope); response.Code != test.want || err != nil {
				t.Fatalf("status=%d body=%s decode=%v", response.Code, raw, err)
			}
			if envelope.Error.Code != test.code {
				t.Fatalf("code=%q want=%q body=%s", envelope.Error.Code, test.code, raw)
			}
			if strings.Contains(string(raw), root) {
				t.Fatalf("error response leaked absolute test path: %s", raw)
			}
		})
	}
}

func TestVideoHandlersMapStorageNotFoundAndValidationErrors(t *testing.T) {
	fixture := newVideoAttemptFixture(t)
	defer fixture.manager.Shutdown(context.Background())

	missing := fixture.request(http.MethodGet, "/api/v1/videos/batches/"+strings.Repeat("a", 32), nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
	invalid := fixture.request(http.MethodPost, "/api/v1/videos/batches", []byte(`{"title":"","execution_kind":"http","preset_id":"sdcpp-video-local","concurrency":1,"common_params":{},"timing":{"mode":"frames","video_frames":1,"fps":1}}`))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("validation status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	created, err := fixture.service.CreateBatch(validHTTPBatchInput("storage failure", "errors"))
	if err != nil {
		t.Fatal(err)
	}
	batchPath := filepath.Join(fixture.root, "batches", created.ID)
	if err := os.RemoveAll(batchPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(batchPath, []byte("blocks batch directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage := fixture.request(http.MethodPut, "/api/v1/videos/batches/"+created.ID, []byte(`{"title":"still private","folder":"errors","execution_kind":"http","preset_id":"sdcpp-video-local","concurrency":1,"common_params":{},"timing":{"mode":"frames","video_frames":1,"fps":1}}`))
	if storage.Code != http.StatusInternalServerError {
		t.Fatalf("storage status=%d body=%s", storage.Code, storage.Body.String())
	}
	if strings.Contains(storage.Body.String(), fixture.root) || !strings.Contains(storage.Body.String(), `"code":"storage_error"`) {
		t.Fatalf("storage response was unsafe or misclassified: %s", storage.Body.String())
	}
}

func validHTTPBatchInput(title, folder string) videogen.CreateBatchInput {
	return videogen.CreateBatchInput{
		Title: title, Folder: folder, ExecutionKind: "http", PresetID: "sdcpp-video-local", Concurrency: 1,
		CommonParams: json.RawMessage(`{}`), Timing: videogen.TimingInput{Mode: "frames", VideoFrames: 1, FPS: 1},
	}
}
