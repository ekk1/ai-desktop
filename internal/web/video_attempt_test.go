package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
	"github.com/ekk1/ai-desktop/internal/videogen"
)

func TestVideoAttemptAPIExecutesGetsAndCancels(t *testing.T) {
	fixture := newVideoAttemptFixture(t)
	defer fixture.manager.Shutdown(context.Background())
	batch, err := fixture.service.CreateBatch(videogen.CreateBatchInput{Title: "video", ExecutionKind: videoconfig.ExecutionHTTP, PresetID: "sdcpp-video-local", Concurrency: 1, CommonParams: json.RawMessage(`{}`), Timing: videogen.TimingInput{Mode: "frames", VideoFrames: 1, FPS: 1}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.CreateItems(batch.ID, []videogen.CreateItemInput{{Prompt: "one", Enabled: true, ParamsOverride: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	r := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+batch.ID+"/items/"+items[0].ID+"/execute", []byte(`{}`))
	var accepted struct {
		Attempts []videogen.Attempt `json:"attempts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&accepted); r.Code != http.StatusAccepted || err != nil || len(accepted.Attempts) != 1 {
		t.Fatalf("execute=%d %s %v", r.Code, r.Body.String(), err)
	}
	id := accepted.Attempts[0].ID
	if got := fixture.request(http.MethodGet, "/api/v1/videos/attempts/"+id, nil); got.Code != http.StatusOK {
		t.Fatalf("get=%d %s", got.Code, got.Body.String())
	}
	if got := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+id+"/cancel", []byte(`{}`)); got.Code != http.StatusAccepted {
		t.Fatalf("cancel=%d %s", got.Code, got.Body.String())
	}
}

type videoRemoteStub struct{}

func (videoRemoteStub) Submit(context.Context, videoconfig.HTTPProvider, []byte) (sdcpp.VideoSubmission, error) {
	return sdcpp.VideoSubmission{ID: "job-video", Kind: "vid_gen", Status: "queued"}, nil
}
func (videoRemoteStub) Job(ctx context.Context, _ videoconfig.HTTPProvider, _ string) (sdcpp.VideoJob, error) {
	<-ctx.Done()
	return sdcpp.VideoJob{}, ctx.Err()
}
func (videoRemoteStub) Cancel(context.Context, videoconfig.HTTPProvider, string) error { return nil }

type videoAttemptFixture struct {
	handler http.Handler
	service *videogen.Service
	manager *videogen.Manager
}

func newVideoAttemptFixture(t *testing.T) videoAttemptFixture {
	t.Helper()
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
	service := videogen.NewService(repo, assets)
	manager := videogen.NewManager(cfg, service, videogen.NewHTTPAssembler(assets), videoRemoteStub{}, videogen.NewWorkspaceManager(filepath.Join(root, "workspace"), assets), videogen.NewCLIExecutor(), assets)
	return videoAttemptFixture{handler: NewHandler(Options{Config: cfg.Snapshot(), ConfigRepository: cfg, AssetRepository: assets, VideoService: service, VideoManager: manager}), service: service, manager: manager}
}
func (f videoAttemptFixture) request(method, path string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRecorder()
	f.handler.ServeHTTP(r, httptest.NewRequest(method, path, bytes.NewReader(body)))
	return r
}
