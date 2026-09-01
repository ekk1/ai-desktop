package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
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
