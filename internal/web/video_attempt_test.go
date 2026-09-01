package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if duplicate := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+batch.ID+"/items/"+items[0].ID+"/execute", []byte(`{}`)); duplicate.Code != http.StatusConflict {
		t.Fatalf("active=%d %s", duplicate.Code, duplicate.Body.String())
	}
	if got := fixture.request(http.MethodGet, "/api/v1/videos/attempts/"+id, nil); got.Code != http.StatusOK {
		t.Fatalf("get=%d %s", got.Code, got.Body.String())
	}
	if got := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+id+"/cancel", []byte(`{}`)); got.Code != http.StatusAccepted {
		t.Fatalf("cancel=%d %s", got.Code, got.Body.String())
	}
	if retry := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+id+"/retry", []byte(`{}`)); retry.Code != http.StatusAccepted {
		t.Fatalf("retry=%d %s", retry.Code, retry.Body.String())
	}
}

func TestVideoCLILogSSEUsesRawOffsetsAndHeartbeat(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	videos := videoconfig.Config{CLIPresets: []videoconfig.CLIPreset{{ID: "local-cli", Name: "Local", Enabled: true, ExecutionKind: videoconfig.ExecutionLocalCLI, CommandTemplate: "sleep 0.05; printf cli-log; sleep 1; printf '\\x1a\\x45\\xdf\\xa3' > {{OUTPUT_PATH}}", WorkDir: "/tmp", Env: map[string]string{}, TimeoutSeconds: 2, StopGraceSeconds: 0, LogBufferBytes: 1024, OutputRelativePath: "outputs/result.webm", OutputMediaType: "video/webm", OutputExtension: ".webm", MaxOutputBytes: 1024, DefaultParams: json.RawMessage(`{}`)}}}
	if _, err := cfg.UpdateVideos(videos); err != nil {
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
	defer manager.Shutdown(context.Background())
	batch, err := service.CreateBatch(videogen.CreateBatchInput{Title: "cli", ExecutionKind: videoconfig.ExecutionLocalCLI, PresetID: "local-cli", Concurrency: 1, CommonParams: json.RawMessage(`{}`), Timing: videogen.TimingInput{Mode: "frames", VideoFrames: 1, FPS: 1}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.CreateItems(batch.ID, []videogen.CreateItemInput{{Prompt: "one", Enabled: true, ParamsOverride: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := manager.StartItem(batch.ID, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := videoAttemptHandler{manager: manager, heartbeat: 5 * time.Millisecond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.logs(w, r, attempt.ID) }))
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first, _ := reader.ReadString('\n')
	data, _ := reader.ReadString('\n')
	_, _ = reader.ReadString('\n')
	if first != "event: snapshot\n" || !strings.Contains(data, `"start_offset"`) || !strings.Contains(data, `"data_base64"`) {
		t.Fatalf("snapshot=%q %q", first, data)
	}
	foundChunk, foundHeartbeat := false, false
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && (!foundChunk || !foundHeartbeat) {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("SSE read: %v", readErr)
		}
		if line == ": heartbeat\n" {
			if blank, err := reader.ReadString('\n'); err != nil || blank != "\n" {
				t.Fatalf("heartbeat terminator=%q err=%v", blank, err)
			}
			foundHeartbeat = true
			continue
		}
		if line != "event: chunk\n" {
			t.Fatalf("unexpected SSE line %q", line)
		}
		chunk, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if blank, err := reader.ReadString('\n'); err != nil || blank != "\n" {
			t.Fatalf("chunk terminator=%q err=%v", blank, err)
		}
		if !strings.Contains(chunk, `"start_offset":0`) || !strings.Contains(chunk, `"data_base64":"Y2xpLWxvZw=="`) {
			t.Fatalf("chunk=%q", chunk)
		}
		foundChunk = true
	}
	if !foundChunk || !foundHeartbeat {
		t.Fatalf("chunk=%v heartbeat=%v", foundChunk, foundHeartbeat)
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
