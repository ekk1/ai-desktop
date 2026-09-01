package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/videogen"
)

func TestVideoBatchAPICreatesAndRejectsNonHexRouteID(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets.json"), filepath.Join(root, "files"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := videogen.OpenRepository(filepath.Join(root, "batches"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{Config: cfg.Snapshot(), ConfigRepository: cfg, AssetRepository: assets, VideoService: videogen.NewService(repo, assets)})
	body := []byte(`{"title":"one","folder":"test","execution_kind":"http","preset_id":"sdcpp-video-local","concurrency":1,"common_params":{},"timing":{"mode":"frames","video_frames":1,"fps":1}}`)
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/api/v1/videos/batches", bytes.NewReader(body)))
	if r.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", r.Code, r.Body.String())
	}
	var created videogen.Batch
	if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/videos/batches?q=one", "/api/v1/videos/batches?folder=test", "/api/v1/videos/batches/" + created.ID} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, response.Code, response.Body.String())
		}
	}
	updated := httptest.NewRecorder()
	h.ServeHTTP(updated, httptest.NewRequest(http.MethodPut, "/api/v1/videos/batches/"+created.ID, bytes.NewBufferString(`{"title":"two","folder":"changed","execution_kind":"http","preset_id":"sdcpp-video-local","concurrency":1,"common_params":{},"timing":{"mode":"frames","video_frames":1,"fps":1}}`)))
	if updated.Code != http.StatusOK {
		t.Fatalf("update=%d %s", updated.Code, updated.Body.String())
	}
	unknown := httptest.NewRecorder()
	h.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/api/v1/videos/batches", bytes.NewBufferString(`{"unknown":true}`)))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown=%d", unknown.Code)
	}
	deleted := httptest.NewRecorder()
	h.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/v1/videos/batches/"+created.ID, nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body.String())
	}
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/videos/batches/not-an-id", nil))
	if r.Code != http.StatusNotFound {
		t.Fatalf("invalid route=%d %s", r.Code, r.Body.String())
	}
}
