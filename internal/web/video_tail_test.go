package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestVideoTailSSESaveAssetSelectionAndCancelContract(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	videos := cfg.Snapshot().Videos
	videos.TailFramePresets = []videoconfig.TailFramePreset{{
		ID: "tail-contract", Name: "Tail Contract", Enabled: true,
		CommandTemplate: "printf tail-log; while [ ! -f $(dirname {{OUTPUT_IMAGE}})/../release ]; do sleep 0.01; done; printf '\\x89PNG\\r\\n\\x1a\\n' > {{OUTPUT_IMAGE}}",
		TimeoutSeconds:  2, StopGraceSeconds: 0, MaxImageBytes: 1024, OutputExtension: ".png",
	}}
	if _, err := cfg.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets.json"), filepath.Join(root, "files"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := assets.Import(asset.ImportInput{Reader: bytes.NewBufferString("video-source"), DisplayName: "source.webm", MediaType: "video/webm"})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := videogen.OpenTailRepository(filepath.Join(root, "videos", "tail-extractions.json"))
	if err != nil {
		t.Fatal(err)
	}
	executor := videogen.NewCLIExecutor()
	extractor := videogen.NewTailExtractor(cfg, repository, assets, executor, filepath.Join(root, "video-workspace"), filepath.Join(root, "videos", "tail-logs"))
	defer executor.Shutdown(context.Background())
	defer extractor.Shutdown(context.Background())
	handler := NewHandler(Options{DataDir: root, Config: cfg.Snapshot(), ConfigRepository: cfg, AssetRepository: assets, TailExtractor: extractor, TailRepository: repository})
	request := func(method, path string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return response
	}

	strictCreate := request(http.MethodPost, "/api/v1/videos/tail-extractions", []byte(`{"source_asset_id":"`+source.ID+`","preset_id":"tail-contract","unknown":true}`))
	if strictCreate.Code != http.StatusBadRequest {
		t.Fatalf("tail create unknown status=%d body=%s", strictCreate.Code, strictCreate.Body.String())
	}
	createdResponse := request(http.MethodPost, "/api/v1/videos/tail-extractions", []byte(`{"source_asset_id":"`+source.ID+`","preset_id":"tail-contract"}`))
	var created videogen.TailExtraction
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); createdResponse.Code != http.StatusAccepted || err != nil || len(created.ID) != 32 || created.SourceAssetID != source.ID || created.PresetID != "tail-contract" || created.State != videogen.AttemptQueued {
		t.Fatalf("create status=%d body=%s decode=%v created=%#v", createdResponse.Code, createdResponse.Body.String(), err, created)
	}

	handlerDone := make(chan struct{})
	eventHandler := videoTailHandler{extractor: extractor, repository: repository, heartbeat: 5 * time.Millisecond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		eventHandler.events(w, r, created.ID)
	}))
	defer server.Close()
	streamContext, cancelStream := context.WithTimeout(context.Background(), 2*time.Second)
	streamRequest, err := http.NewRequestWithContext(streamContext, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	streamResponse, err := http.DefaultClient.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResponse.Body.Close()
	reader := bufio.NewReader(streamResponse.Body)
	eventName, data, heartbeat := readVideoSSEFrame(t, reader)
	var snapshot videogen.TailExtraction
	if eventName != "snapshot" || heartbeat || json.Unmarshal(data, &snapshot) != nil || snapshot.ID != created.ID || snapshot.SourceAssetID != source.ID || snapshot.PresetID != "tail-contract" || (snapshot.State != videogen.AttemptQueued && snapshot.State != videogen.AttemptRunning) {
		t.Fatalf("snapshot event=%q heartbeat=%v data=%s decoded=%#v", eventName, heartbeat, data, snapshot)
	}
	foundHeartbeat := false
	for !foundHeartbeat {
		eventName, data, heartbeat = readVideoSSEFrame(t, reader)
		if heartbeat {
			foundHeartbeat = true
			continue
		}
		if eventName != "state" {
			t.Fatalf("pre-release event=%q data=%s", eventName, data)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "video-workspace", "tail-"+created.ID, "release"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	var succeeded videogen.TailExtraction
	for succeeded.State != videogen.AttemptSucceeded {
		eventName, data, heartbeat = readVideoSSEFrame(t, reader)
		if heartbeat {
			continue
		}
		if eventName != "state" || json.Unmarshal(data, &succeeded) != nil {
			t.Fatalf("tail state event=%q data=%s", eventName, data)
		}
		if succeeded.ID != created.ID {
			t.Fatalf("tail state ID=%s want=%s", succeeded.ID, created.ID)
		}
	}
	if len(succeeded.OutputAssetID) != 32 || succeeded.CompletedAt == nil {
		t.Fatalf("succeeded extraction=%#v", succeeded)
	}
	cancelStream()
	_ = streamResponse.Body.Close()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("tail SSE handler did not return after request context cancellation")
	}
	if subscribers := reflect.ValueOf(extractor).Elem().FieldByName("subscribers").Len(); subscribers != 0 {
		t.Fatalf("tail SSE subscriptions after cancellation=%d, want 0", subscribers)
	}

	strictSave := request(http.MethodPost, "/api/v1/videos/tail-extractions/"+created.ID+"/logs/save", []byte(`{"unknown":true}`))
	if strictSave.Code != http.StatusBadRequest {
		t.Fatalf("tail save unknown status=%d body=%s", strictSave.Code, strictSave.Body.String())
	}
	savedResponse := request(http.MethodPost, "/api/v1/videos/tail-extractions/"+created.ID+"/logs/save", []byte(`{}`))
	var saved struct {
		WorkspacePath string `json:"workspace_path"`
	}
	if err := json.NewDecoder(savedResponse.Body).Decode(&saved); savedResponse.Code != http.StatusOK || err != nil {
		t.Fatalf("tail save status=%d body=%s decode=%v", savedResponse.Code, savedResponse.Body.String(), err)
	}
	wantLocation := "videos/tail-logs/" + created.ID + ".log"
	if saved.WorkspacePath != wantLocation || strings.Contains(saved.WorkspacePath, root) {
		t.Fatalf("tail log location=%q want=%q root=%q", saved.WorkspacePath, wantLocation, root)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(saved.WorkspacePath))); err != nil {
		t.Fatalf("saved tail log missing: %v", err)
	}

	selectedResponse := request(http.MethodPost, "/api/v1/assets/"+succeeded.OutputAssetID+"/state", []byte(`{"state":"active"}`))
	var selected asset.Asset
	if err := json.NewDecoder(selectedResponse.Body).Decode(&selected); selectedResponse.Code != http.StatusOK || err != nil || selected.ID != succeeded.OutputAssetID || selected.State != asset.StateActive {
		t.Fatalf("select asset status=%d body=%s decode=%v selected=%#v", selectedResponse.Code, selectedResponse.Body.String(), err, selected)
	}
	getAsset := request(http.MethodGet, "/api/v1/assets/"+succeeded.OutputAssetID, nil)
	var fetched asset.Asset
	if err := json.NewDecoder(getAsset.Body).Decode(&fetched); getAsset.Code != http.StatusOK || err != nil || fetched.ID != succeeded.OutputAssetID || fetched.State != asset.StateActive || fetched.MediaType != "image/png" {
		t.Fatalf("GET asset status=%d body=%s decode=%v fetched=%#v", getAsset.Code, getAsset.Body.String(), err, fetched)
	}

	cancelCreatedResponse := request(http.MethodPost, "/api/v1/videos/tail-extractions", []byte(`{"source_asset_id":"`+source.ID+`","preset_id":"tail-contract"}`))
	var cancelCreated videogen.TailExtraction
	if err := json.NewDecoder(cancelCreatedResponse.Body).Decode(&cancelCreated); cancelCreatedResponse.Code != http.StatusAccepted || err != nil {
		t.Fatalf("create cancellation target status=%d body=%s decode=%v", cancelCreatedResponse.Code, cancelCreatedResponse.Body.String(), err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		status, statusErr := executor.Status(cancelCreated.ID)
		if statusErr == nil && status.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tail cancellation target did not start: status=%#v err=%v", status, statusErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	strictCancel := request(http.MethodPost, "/api/v1/videos/tail-extractions/"+cancelCreated.ID+"/cancel", []byte(`{"unknown":true}`))
	if strictCancel.Code != http.StatusBadRequest {
		t.Fatalf("tail cancel unknown status=%d body=%s", strictCancel.Code, strictCancel.Body.String())
	}
	cancelResponse := request(http.MethodPost, "/api/v1/videos/tail-extractions/"+cancelCreated.ID+"/cancel", []byte(`{}`))
	var cancelled videogen.TailExtraction
	if err := json.NewDecoder(cancelResponse.Body).Decode(&cancelled); cancelResponse.Code != http.StatusAccepted || err != nil || cancelled.ID != cancelCreated.ID || cancelled.State != videogen.AttemptCancelled || cancelled.CompletedAt == nil {
		t.Fatalf("tail cancel status=%d body=%s decode=%v cancelled=%#v", cancelResponse.Code, cancelResponse.Body.String(), err, cancelled)
	}
}
