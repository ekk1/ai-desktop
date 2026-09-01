package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
	"github.com/ekk1/ai-desktop/internal/videogen"
)

func TestVideoTailGETUsesTailRepository(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets.json"), filepath.Join(root, "files"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := videogen.OpenTailRepository(filepath.Join(root, "videos", "tail-extractions.json"))
	if err != nil {
		t.Fatal(err)
	}
	extractor := videogen.NewTailExtractor(cfg, repo, assets, videogen.NewCLIExecutor(), filepath.Join(root, "workspaces"), filepath.Join(root, "logs"))
	handler := NewHandler(Options{Config: cfg.Snapshot(), ConfigRepository: cfg, AssetRepository: assets, TailExtractor: extractor, TailRepository: repo})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/videos/tail-extractions/"+strings.Repeat("a", 32), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("GET status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestVideoTailMissingSourceAssetMapsToNotFound(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets.json"), filepath.Join(root, "files"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := videogen.OpenTailRepository(filepath.Join(root, "videos", "tail-extractions.json"))
	if err != nil {
		t.Fatal(err)
	}
	extractor := videogen.NewTailExtractor(cfg, repo, assets, videogen.NewCLIExecutor(), filepath.Join(root, "workspaces"), filepath.Join(root, "logs"))
	handler := NewHandler(Options{Config: cfg.Snapshot(), ConfigRepository: cfg, AssetRepository: assets, TailExtractor: extractor, TailRepository: repo})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/videos/tail-extractions", bytes.NewBufferString(`{"source_asset_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","preset_id":"missing"}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("POST status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestVideoTailAPICreatesGetsAndCancels(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	videos := cfg.Snapshot().Videos
	videos.TailFramePresets = []videoconfig.TailFramePreset{{ID: "tail", Name: "Tail", Enabled: true, CommandTemplate: "sleep 1; printf '\\x89PNG\\r\\n\\x1a\\n' > {{OUTPUT_IMAGE}}", TimeoutSeconds: 2, StopGraceSeconds: 0, MaxImageBytes: 1024, OutputExtension: ".png"}}
	if _, err := cfg.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets.json"), filepath.Join(root, "files"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := assets.Import(asset.ImportInput{Reader: bytes.NewBufferString("video"), DisplayName: "source.webm", MediaType: "video/webm"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := videogen.OpenTailRepository(filepath.Join(root, "videos", "tail-extractions.json"))
	if err != nil {
		t.Fatal(err)
	}
	executor := videogen.NewCLIExecutor()
	extractor := videogen.NewTailExtractor(cfg, repo, assets, executor, filepath.Join(root, "workspace"), filepath.Join(root, "logs"))
	defer extractor.Shutdown(context.Background())
	defer executor.Shutdown(context.Background())
	h := NewHandler(Options{DataDir: root, Config: cfg.Snapshot(), ConfigRepository: cfg, AssetRepository: assets, TailExtractor: extractor, TailRepository: repo})
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/api/v1/videos/tail-extractions", bytes.NewBufferString(`{"source_asset_id":"`+source.ID+`","preset_id":"tail"}`)))
	if r.Code != http.StatusAccepted {
		t.Fatalf("create=%d %s", r.Code, r.Body.String())
	}
	var extraction videogen.TailExtraction
	if err := json.NewDecoder(r.Body).Decode(&extraction); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/videos/tail-extractions/"+extraction.ID, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("get=%d", get.Code)
	}
	cancel := httptest.NewRecorder()
	h.ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, "/api/v1/videos/tail-extractions/"+extraction.ID+"/cancel", bytes.NewBufferString(`{}`)))
	if cancel.Code != http.StatusAccepted {
		t.Fatalf("cancel=%d %s", cancel.Code, cancel.Body.String())
	}
}
