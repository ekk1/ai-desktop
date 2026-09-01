package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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

func TestVideoTailLogSSEUsesRawOffsetsHeartbeatAndRequestCancellation(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	videos := cfg.Snapshot().Videos
	videos.TailFramePresets = []videoconfig.TailFramePreset{{
		ID: "tail-raw-log", Name: "Tail Raw Log", Enabled: true,
		CommandTemplate: "while [ ! -f $(dirname {{OUTPUT_IMAGE}})/../release ]; do sleep 0.01; done; printf 'tail-\\377-log'; printf '\\x89PNG\\r\\n\\x1a\\n' > {{OUTPUT_IMAGE}}",
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
	handler := videoTailHandler{extractor: extractor, repository: repository, heartbeat: 5 * time.Millisecond}

	created, err := extractor.Extract(context.Background(), source.ID, "tail-raw-log")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		status, statusErr := executor.Status(created.ID)
		if statusErr == nil && status.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tail extraction did not start: status=%#v err=%v", status, statusErr)
		}
		time.Sleep(5 * time.Millisecond)
	}

	for _, check := range []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"invalid ID", http.MethodGet, "/api/v1/videos/tail-extractions/not-an-id/logs", http.StatusNotFound},
		{"missing extraction", http.MethodGet, "/api/v1/videos/tail-extractions/ffffffffffffffffffffffffffffffff/logs", http.StatusNotFound},
		{"wrong method", http.MethodPost, "/api/v1/videos/tail-extractions/" + created.ID + "/logs", http.StatusMethodNotAllowed},
	} {
		recorder := httptest.NewRecorder()
		handler.serve(recorder, httptest.NewRequest(check.method, check.path, nil))
		if recorder.Code != check.status {
			t.Fatalf("%s status=%d body=%s, want %d", check.name, recorder.Code, recorder.Body.String(), check.status)
		}
	}

	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		handler.serve(w, r)
	}))
	defer server.Close()
	streamContext, cancelStream := context.WithTimeout(context.Background(), 2*time.Second)
	request, err := http.NewRequestWithContext(streamContext, http.MethodGet, server.URL+"/api/v1/videos/tail-extractions/"+created.ID+"/logs", nil)
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
	var snapshot struct {
		CapacityBytes int    `json:"capacity_bytes"`
		StartOffset   int64  `json:"start_offset"`
		EndOffset     int64  `json:"end_offset"`
		DataBase64    string `json:"data_base64"`
	}
	if eventName != "snapshot" || heartbeat || json.Unmarshal(data, &snapshot) != nil || snapshot.CapacityBytes != 64<<10 || snapshot.StartOffset != 0 || snapshot.EndOffset != 0 || snapshot.DataBase64 != "" {
		t.Fatalf("snapshot event=%q heartbeat=%v data=%s decoded=%#v", eventName, heartbeat, data, snapshot)
	}
	if err := os.WriteFile(filepath.Join(root, "video-workspace", "tail-"+created.ID, "release"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantLog := []byte{'t', 'a', 'i', 'l', '-', 0xff, '-', 'l', 'o', 'g'}
	foundChunk, foundHeartbeat := false, false
	for !foundChunk || !foundHeartbeat {
		eventName, data, heartbeat = readVideoSSEFrame(t, reader)
		if heartbeat {
			foundHeartbeat = true
			continue
		}
		if eventName != "chunk" {
			t.Fatalf("log event=%q data=%s", eventName, data)
		}
		var chunk struct {
			StartOffset int64  `json:"start_offset"`
			DataBase64  string `json:"data_base64"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(chunk.DataBase64)
		if err != nil || chunk.StartOffset != 0 || !bytes.Equal(decoded, wantLog) {
			t.Fatalf("chunk=%#v decoded=%v decode=%v, want offset=0 data=%v", chunk, decoded, err, wantLog)
		}
		foundChunk = true
	}
	cancelStream()
	_ = response.Body.Close()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("tail log SSE handler did not return after request context cancellation")
	}

	deadline = time.Now().Add(time.Second)
	for {
		terminal, ok := repository.Get(created.ID)
		if ok && terminal.State == videogen.AttemptSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tail extraction did not reach succeeded after log stream: %#v", terminal)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(root, "video-workspace", "tail-"+created.ID)); !os.IsNotExist(err) {
		t.Fatalf("terminal Tail workspace still exists or could not be checked: %v", err)
	}

	terminalDone := make(chan struct{})
	terminalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(terminalDone)
		handler.serve(w, r)
	}))
	terminalContext, cancelTerminal := context.WithCancel(context.Background())
	terminalRequest, err := http.NewRequestWithContext(terminalContext, http.MethodGet, terminalServer.URL+"/api/v1/videos/tail-extractions/"+created.ID+"/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	terminalResponse, err := http.DefaultClient.Do(terminalRequest)
	if err != nil {
		t.Fatal(err)
	}
	terminalReader := bufio.NewReader(terminalResponse.Body)
	eventName, data, heartbeat = readVideoSSEFrame(t, terminalReader)
	if eventName != "snapshot" || heartbeat || json.Unmarshal(data, &snapshot) != nil {
		t.Fatalf("terminal snapshot event=%q heartbeat=%v data=%s decoded=%#v", eventName, heartbeat, data, snapshot)
	}
	decodedSnapshot, err := base64.StdEncoding.DecodeString(snapshot.DataBase64)
	if err != nil || snapshot.StartOffset != 0 || snapshot.EndOffset != int64(len(wantLog)) || !bytes.Equal(decodedSnapshot, wantLog) {
		t.Fatalf("terminal snapshot=%#v decoded=%v decode=%v, want offsets=0-%d data=%v", snapshot, decodedSnapshot, err, len(wantLog), wantLog)
	}
	cancelTerminal()
	_ = terminalResponse.Body.Close()
	select {
	case <-terminalDone:
	case <-time.After(time.Second):
		t.Fatal("terminal Tail log SSE handler did not unsubscribe after request cancellation")
	}
	terminalServer.Close()
}
