package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	if len(id) != 32 || accepted.Attempts[0].BatchID != batch.ID || accepted.Attempts[0].ItemID != items[0].ID || accepted.Attempts[0].State != videogen.AttemptQueued {
		t.Fatalf("accepted attempt=%#v", accepted.Attempts[0])
	}
	if duplicate := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+batch.ID+"/items/"+items[0].ID+"/execute", []byte(`{}`)); duplicate.Code != http.StatusConflict {
		t.Fatalf("active=%d %s", duplicate.Code, duplicate.Body.String())
	}
	got := fixture.request(http.MethodGet, "/api/v1/videos/attempts/"+id, nil)
	var fetched videogen.Attempt
	if err := json.NewDecoder(got.Body).Decode(&fetched); got.Code != http.StatusOK || err != nil || fetched.ID != id || fetched.BatchID != batch.ID || fetched.ItemID != items[0].ID || terminalVideoAttempt(fetched.State) {
		t.Fatalf("get=%d %s decode=%v fetched=%#v", got.Code, got.Body.String(), err, fetched)
	}
	if strict := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+id+"/cancel", []byte(`{"unknown":true}`)); strict.Code != http.StatusBadRequest {
		t.Fatalf("cancel unknown=%d %s", strict.Code, strict.Body.String())
	}
	cancelledResponse := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+id+"/cancel", []byte(`{}`))
	var cancelled videogen.Attempt
	if err := json.NewDecoder(cancelledResponse.Body).Decode(&cancelled); cancelledResponse.Code != http.StatusAccepted || err != nil || cancelled.ID != id || cancelled.State != videogen.AttemptCancelled {
		t.Fatalf("cancel=%d %s decode=%v cancelled=%#v", cancelledResponse.Code, cancelledResponse.Body.String(), err, cancelled)
	}
	if strict := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+id+"/retry", []byte(`{"unknown":true}`)); strict.Code != http.StatusBadRequest {
		t.Fatalf("retry unknown=%d %s", strict.Code, strict.Body.String())
	}
	retryResponse := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+id+"/retry", []byte(`{}`))
	var retried videogen.Attempt
	if err := json.NewDecoder(retryResponse.Body).Decode(&retried); retryResponse.Code != http.StatusAccepted || err != nil || retried.ID == id || len(retried.ID) != 32 || retried.BatchID != batch.ID || retried.ItemID != items[0].ID || retried.State != videogen.AttemptQueued {
		t.Fatalf("retry=%d %s decode=%v retried=%#v", retryResponse.Code, retryResponse.Body.String(), err, retried)
	}
	if cleanup := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+retried.ID+"/cancel", []byte(`{}`)); cleanup.Code != http.StatusAccepted {
		t.Fatalf("cancel retry=%d %s", cleanup.Code, cleanup.Body.String())
	}
}

func TestVideoAttemptCancelReleasesRequestScopedRemoteCancellation(t *testing.T) {
	remote := newBlockingVideoCancelRemote()
	fixture := newVideoAttemptFixtureWithRemote(t, remote)
	defer fixture.manager.Shutdown(context.Background())
	batch, err := fixture.service.CreateBatch(videogen.CreateBatchInput{Title: "video", ExecutionKind: videoconfig.ExecutionHTTP, PresetID: "sdcpp-video-local", Concurrency: 1, CommonParams: json.RawMessage(`{}`), Timing: videogen.TimingInput{Mode: "frames", VideoFrames: 1, FPS: 1}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.CreateItems(batch.ID, []videogen.CreateItemInput{{Prompt: "one", Enabled: true, ParamsOverride: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := fixture.manager.StartItem(batch.ID, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current, ok := fixture.manager.GetAttempt(attempt.ID)
		if ok && current.State == videogen.AttemptPolling {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("remote attempt did not reach polling")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/videos/attempts/"+attempt.ID+"/cancel", bytes.NewReader([]byte(`{}`))).WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		fixture.handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-remote.cancelStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not begin remote cancellation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cancel handler remained blocked after its request context ended")
	}
}

func TestVideoCLILogSSEUsesRawOffsetsAndHeartbeat(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	videos := videoconfig.Config{CLIPresets: []videoconfig.CLIPreset{{ID: "local-cli", Name: "Local", Enabled: true, ExecutionKind: videoconfig.ExecutionLocalCLI, CommandTemplate: "while [ ! -f {{WORKSPACE_DIR}}/release ]; do sleep 0.01; done; printf cli-log; sleep 1; printf '\\x1a\\x45\\xdf\\xa3' > {{OUTPUT_PATH}}", WorkDir: "/tmp", Env: map[string]string{}, TimeoutSeconds: 2, StopGraceSeconds: 0, LogBufferBytes: 1024, OutputRelativePath: "outputs/result.webm", OutputMediaType: "video/webm", OutputExtension: ".webm", MaxOutputBytes: 1024, DefaultParams: json.RawMessage(`{}`)}}}
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
	streamCtx, cancelStream := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelStream()
	request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
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
	if err := os.WriteFile(filepath.Join(root, "workspace", attempt.ID, "release"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	foundChunk, foundHeartbeat := false, false
	deadline := time.Now().Add(time.Second)
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

func TestVideoBatchSSESnapshotStateHeartbeatAndCancellation(t *testing.T) {
	fixture := newVideoAttemptFixture(t)
	defer fixture.manager.Shutdown(context.Background())
	batch, err := fixture.service.CreateBatch(videogen.CreateBatchInput{Title: "events", ExecutionKind: videoconfig.ExecutionHTTP, PresetID: "sdcpp-video-local", Concurrency: 1, CommonParams: json.RawMessage(`{}`), Timing: videogen.TimingInput{Mode: "frames", VideoFrames: 1, FPS: 1}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.CreateItems(batch.ID, []videogen.CreateItemInput{{Prompt: "event item", Enabled: true, ParamsOverride: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}

	handlerDone := make(chan struct{})
	handler := videoAttemptHandler{manager: fixture.manager, heartbeat: 5 * time.Millisecond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		handler.events(w, r, batch.ID)
	}))
	defer server.Close()
	streamContext, cancelStream := context.WithTimeout(context.Background(), 2*time.Second)
	request, err := http.NewRequestWithContext(streamContext, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	eventName, data, heartbeat := readVideoSSEFrame(t, reader)
	var snapshot videogen.AttemptEvent
	if eventName != "snapshot" || heartbeat || json.Unmarshal(data, &snapshot) != nil || snapshot.Type != "snapshot" || snapshot.BatchID != batch.ID || len(snapshot.Attempts) != 0 {
		t.Fatalf("snapshot event=%q heartbeat=%v data=%s decoded=%#v", eventName, heartbeat, data, snapshot)
	}

	attempt, err := fixture.manager.StartItem(batch.ID, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	foundState, foundHeartbeat := false, false
	for !foundState || !foundHeartbeat {
		eventName, data, heartbeat = readVideoSSEFrame(t, reader)
		if heartbeat {
			foundHeartbeat = true
			continue
		}
		if eventName != "state" {
			t.Fatalf("unexpected event=%q data=%s", eventName, data)
		}
		var state videogen.AttemptEvent
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		if state.Type != "state" || state.BatchID != batch.ID || state.Attempt.ID != attempt.ID || state.Attempt.ItemID != items[0].ID {
			t.Fatalf("state event=%#v", state)
		}
		foundState = true
	}

	if err := fixture.manager.Cancel(attempt.ID); err != nil {
		t.Fatal(err)
	}
	cancelStream()
	_ = response.Body.Close()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("batch SSE handler did not return after request context cancellation")
	}
	if subscribers := reflect.ValueOf(fixture.manager).Elem().FieldByName("subscribers").Len(); subscribers != 0 {
		t.Fatalf("batch SSE subscriptions after cancellation=%d, want 0", subscribers)
	}
}

func TestVideoCLIWorkspaceSaveCleanupAndNoClearEndpointContract(t *testing.T) {
	fixture := newVideoAttemptFixture(t)
	defer fixture.manager.Shutdown(context.Background())
	videos := fixture.config.Snapshot().Videos
	videos.CLIPresets = []videoconfig.CLIPreset{{
		ID: "cli-workspace", Name: "CLI Workspace", Enabled: true, ExecutionKind: videoconfig.ExecutionLocalCLI,
		CommandTemplate: "printf cli-log; while [ ! -f {{WORKSPACE_DIR}}/release ]; do sleep 0.01; done; printf '\\x1a\\x45\\xdf\\xa3' > {{OUTPUT_PATH}}",
		WorkDir:         "/tmp", Env: map[string]string{}, TimeoutSeconds: 2, StopGraceSeconds: 0, LogBufferBytes: 1024,
		OutputRelativePath: "outputs/result.webm", OutputMediaType: "video/webm", OutputExtension: ".webm", MaxOutputBytes: 1024, DefaultParams: json.RawMessage(`{}`),
	}}
	if _, err := fixture.config.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	batch, err := fixture.service.CreateBatch(videogen.CreateBatchInput{Title: "cli workspace", ExecutionKind: videoconfig.ExecutionLocalCLI, PresetID: "cli-workspace", Concurrency: 1, CommonParams: json.RawMessage(`{}`), Timing: videogen.TimingInput{Mode: "frames", VideoFrames: 1, FPS: 1}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.CreateItems(batch.ID, []videogen.CreateItemInput{{Prompt: "one", Enabled: true, ParamsOverride: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	execute := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+batch.ID+"/items/"+items[0].ID+"/execute", []byte(`{}`))
	var accepted struct {
		Attempts []videogen.Attempt `json:"attempts"`
	}
	if err := json.NewDecoder(execute.Body).Decode(&accepted); execute.Code != http.StatusAccepted || err != nil || len(accepted.Attempts) != 1 {
		t.Fatalf("execute status=%d body=%s decode=%v", execute.Code, execute.Body.String(), err)
	}
	attemptID := accepted.Attempts[0].ID
	deadline := time.Now().Add(time.Second)
	for {
		status, statusErr := fixture.executor.Status(attemptID)
		if statusErr == nil && status.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("CLI did not start: status=%#v err=%v", status, statusErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	workspace := filepath.Join(fixture.root, "video-workspace", attemptID)
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		t.Fatalf("workspace %s info=%v err=%v", workspace, info, err)
	}

	strictSave := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+attemptID+"/logs/save", []byte(`{"unknown":true}`))
	if strictSave.Code != http.StatusBadRequest {
		t.Fatalf("save unknown status=%d body=%s", strictSave.Code, strictSave.Body.String())
	}
	savedResponse := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+attemptID+"/logs/save", []byte(`{}`))
	var saved struct {
		WorkspacePath string `json:"workspace_path"`
	}
	if err := json.NewDecoder(savedResponse.Body).Decode(&saved); savedResponse.Code != http.StatusOK || err != nil {
		t.Fatalf("save status=%d body=%s decode=%v", savedResponse.Code, savedResponse.Body.String(), err)
	}
	wantLocation := "video-logs/" + attemptID + ".log"
	if saved.WorkspacePath != wantLocation || strings.Contains(saved.WorkspacePath, fixture.root) {
		t.Fatalf("saved location=%q want=%q root=%q", saved.WorkspacePath, wantLocation, fixture.root)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, filepath.FromSlash(saved.WorkspacePath))); err != nil {
		t.Fatalf("saved log missing: %v", err)
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		clear := fixture.request(method, "/api/v1/videos/attempts/"+attemptID+"/logs/clear", []byte(`{}`))
		if clear.Code != http.StatusNotFound {
			t.Fatalf("%s clear endpoint status=%d body=%s", method, clear.Code, clear.Body.String())
		}
	}
	activeCleanup := fixture.request(http.MethodDelete, "/api/v1/videos/attempts/"+attemptID+"/workspace", nil)
	if activeCleanup.Code != http.StatusConflict {
		t.Fatalf("active cleanup status=%d body=%s", activeCleanup.Code, activeCleanup.Body.String())
	}
	cancelResponse := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+attemptID+"/cancel", []byte(`{}`))
	var cancelled videogen.Attempt
	if err := json.NewDecoder(cancelResponse.Body).Decode(&cancelled); cancelResponse.Code != http.StatusAccepted || err != nil || cancelled.ID != attemptID || cancelled.State != videogen.AttemptCancelled {
		t.Fatalf("cancel status=%d body=%s decode=%v cancelled=%#v", cancelResponse.Code, cancelResponse.Body.String(), err, cancelled)
	}
	cleanup := fixture.request(http.MethodDelete, "/api/v1/videos/attempts/"+attemptID+"/workspace", nil)
	if cleanup.Code != http.StatusNoContent {
		t.Fatalf("terminal cleanup status=%d body=%s", cleanup.Code, cleanup.Body.String())
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after cleanup: %v", err)
	}
}

func readVideoSSEFrame(t *testing.T, reader *bufio.Reader) (string, []byte, bool) {
	t.Helper()
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE event line: %v", err)
	}
	if line == ": heartbeat\n" {
		if blank, err := reader.ReadString('\n'); err != nil || blank != "\n" {
			t.Fatalf("heartbeat terminator=%q err=%v", blank, err)
		}
		return "", nil, true
	}
	if !strings.HasPrefix(line, "event: ") {
		t.Fatalf("unexpected SSE line %q", line)
	}
	event := strings.TrimSpace(strings.TrimPrefix(line, "event: "))
	dataLine, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("SSE data=%q err=%v", dataLine, err)
	}
	if blank, err := reader.ReadString('\n'); err != nil || blank != "\n" {
		t.Fatalf("SSE terminator=%q err=%v", blank, err)
	}
	return event, []byte(strings.TrimSpace(strings.TrimPrefix(dataLine, "data: "))), false
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

type blockingVideoCancelRemote struct {
	cancelStarted chan struct{}
	cancelOnce    sync.Once
}

func newBlockingVideoCancelRemote() *blockingVideoCancelRemote {
	return &blockingVideoCancelRemote{cancelStarted: make(chan struct{})}
}

func (remote *blockingVideoCancelRemote) Submit(context.Context, videoconfig.HTTPProvider, []byte) (sdcpp.VideoSubmission, error) {
	return sdcpp.VideoSubmission{ID: "job-blocking-cancel", Kind: "vid_gen", Status: "queued"}, nil
}

func (remote *blockingVideoCancelRemote) Job(context.Context, videoconfig.HTTPProvider, string) (sdcpp.VideoJob, error) {
	return sdcpp.VideoJob{ID: "job-blocking-cancel", Status: "generating", QueuePosition: 1}, nil
}

func (remote *blockingVideoCancelRemote) Cancel(ctx context.Context, _ videoconfig.HTTPProvider, _ string) error {
	remote.cancelOnce.Do(func() { close(remote.cancelStarted) })
	<-ctx.Done()
	return fmt.Errorf("blocked remote cancel: %w", ctx.Err())
}

type videoAttemptFixture struct {
	handler  http.Handler
	service  *videogen.Service
	manager  *videogen.Manager
	config   *config.Repository
	assets   *asset.Repository
	executor *videogen.CLIExecutor
	root     string
}

func newVideoAttemptFixture(t *testing.T) videoAttemptFixture {
	return newVideoAttemptFixtureWithRemote(t, videoRemoteStub{})
}

func newVideoAttemptFixtureWithRemote(t *testing.T, remote videogen.VideoRemoteClient) videoAttemptFixture {
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
	executor := videogen.NewCLIExecutor()
	manager := videogen.NewManager(cfg, service, videogen.NewHTTPAssembler(assets), remote, videogen.NewWorkspaceManager(filepath.Join(root, "video-workspace"), assets), executor, assets)
	return videoAttemptFixture{
		handler: NewHandler(Options{DataDir: root, Config: cfg.Snapshot(), ConfigRepository: cfg, AssetRepository: assets, VideoService: service, VideoManager: manager}),
		service: service, manager: manager, config: cfg, assets: assets, executor: executor, root: root,
	}
}
func (f videoAttemptFixture) request(method, path string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRecorder()
	f.handler.ServeHTTP(r, httptest.NewRequest(method, path, bytes.NewReader(body)))
	return r
}
