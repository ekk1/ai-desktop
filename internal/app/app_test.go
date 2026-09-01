package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/backend"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
	"github.com/ekk1/ai-desktop/internal/videogen"
)

func TestNewServerAlwaysUsesLoopbackAddress(t *testing.T) {
	cfg := config.Default()
	dataDir := t.TempDir()

	server, err := NewServer(dataDir, cfg, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := server.Addr, "127.0.0.1:8188"; got != want {
		t.Fatalf("Addr = %q, want %q", got, want)
	}

	server, err = NewServer(dataDir, cfg, "test", 9001)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := server.Addr, "127.0.0.1:9001"; got != want {
		t.Fatalf("Addr = %q, want %q", got, want)
	}
}

func TestApplicationOpensVideoPersistencePaths(t *testing.T) {
	dataDir := t.TempDir()
	runtime, err := newRuntime(dataDir, config.Default(), "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.shutdownManagers(shutdownContext); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dataDir, "videos", "batches"),
		filepath.Join(dataDir, "videos", "tail-extractions.json"),
		filepath.Join(dataDir, "video-workspace"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("video path %q: %v", path, err)
		}
	}
}

func TestApplicationReopenInterruptsVideoAttemptAfterRemoteCancelConflict(t *testing.T) {
	pollStarted := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sdcpp/v1/vid_gen":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"video-app","kind":"vid_gen","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/sdcpp/v1/jobs/video-app":
			select {
			case <-pollStarted:
			default:
				close(pollStarted)
			}
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/sdcpp/v1/jobs/video-app/cancel":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.Videos.HTTPProviders[0].BaseURL = provider.URL
	runtime, err := newRuntime(dataDir, cfg, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1/videos/batches", bytes.NewBufferString(`{"title":"video","execution_kind":"http","preset_id":"sdcpp-video-local","concurrency":1,"common_params":{},"timing":{"mode":"frames","video_frames":1,"fps":1}}`)))
	var batch struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&batch); err != nil || create.Code != http.StatusCreated {
		t.Fatalf("batch=%d %s %v", create.Code, create.Body.String(), err)
	}
	items := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(items, httptest.NewRequest(http.MethodPost, "/api/v1/videos/batches/"+batch.ID+"/items", bytes.NewBufferString(`{"items":[{"prompt":"one","enabled":true,"params_override":{}}]}`)))
	var added struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(items.Body).Decode(&added); err != nil || items.Code != http.StatusCreated || len(added.Items) != 1 {
		t.Fatalf("items=%d %s %v", items.Code, items.Body.String(), err)
	}
	execute := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(execute, httptest.NewRequest(http.MethodPost, "/api/v1/videos/batches/"+batch.ID+"/items/"+added.Items[0].ID+"/execute", bytes.NewBufferString(`{}`)))
	var accepted struct {
		Attempts []struct {
			ID string `json:"id"`
		} `json:"attempts"`
	}
	if err := json.NewDecoder(execute.Body).Decode(&accepted); err != nil || execute.Code != http.StatusAccepted || len(accepted.Attempts) != 1 {
		t.Fatalf("execute=%d %s %v", execute.Code, execute.Body.String(), err)
	}
	select {
	case <-pollStarted:
	case <-time.After(time.Second):
		t.Fatal("video polling did not start")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = runtime.shutdownManagers(shutdownContext)
	reopened, err := newRuntime(dataDir, cfg, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.shutdownManagers(context.Background())
	get := httptest.NewRecorder()
	reopened.server.Handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/videos/attempts/"+accepted.Attempts[0].ID, nil))
	var attempt videogen.Attempt
	if err := json.NewDecoder(get.Body).Decode(&attempt); err != nil || get.Code != http.StatusOK || attempt.State != videogen.AttemptInterrupted {
		t.Fatalf("attempt=%#v status=%d err=%v", attempt, get.Code, err)
	}
}

func TestApplicationShutsDownSharedVideoCLIAndTailExecutor(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.Videos.CLIPresets = []videoconfig.CLIPreset{{ID: "cli", Name: "CLI", Enabled: true, ExecutionKind: videoconfig.ExecutionLocalCLI, CommandTemplate: "trap 'exit 0' TERM; while :; do sleep 1; done; : {{OUTPUT_PATH}}", WorkDir: "/tmp", Env: map[string]string{}, TimeoutSeconds: 30, StopGraceSeconds: 0, LogBufferBytes: 1024, OutputRelativePath: "outputs/result.webm", OutputMediaType: "video/webm", OutputExtension: ".webm", MaxOutputBytes: 1024, DefaultParams: json.RawMessage(`{}`)}}
	cfg.Videos.TailFramePresets = []videoconfig.TailFramePreset{{ID: "tail", Name: "Tail", Enabled: true, CommandTemplate: "trap 'exit 0' TERM; while :; do sleep 1; done; : {{OUTPUT_IMAGE}}", TimeoutSeconds: 30, StopGraceSeconds: 0, MaxImageBytes: 1024, OutputExtension: ".png"}}
	runtime, err := newRuntime(dataDir, cfg, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = runtime.shutdownManagers(ctx)
	})
	serve := runtime.server.Handler
	batchResponse := httptest.NewRecorder()
	serve.ServeHTTP(batchResponse, httptest.NewRequest(http.MethodPost, "/api/v1/videos/batches", bytes.NewBufferString(`{"title":"cli","execution_kind":"local_cli","preset_id":"cli","concurrency":1,"common_params":{},"timing":{"mode":"frames","video_frames":1,"fps":1}}`)))
	var batch struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(batchResponse.Body).Decode(&batch); err != nil || batchResponse.Code != 201 {
		t.Fatalf("batch %d %v %s", batchResponse.Code, err, batchResponse.Body.String())
	}
	itemResponse := httptest.NewRecorder()
	serve.ServeHTTP(itemResponse, httptest.NewRequest(http.MethodPost, "/api/v1/videos/batches/"+batch.ID+"/items", bytes.NewBufferString(`{"items":[{"prompt":"run","enabled":true,"params_override":{}}]}`)))
	var items struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(itemResponse.Body).Decode(&items); err != nil || len(items.Items) != 1 {
		t.Fatalf("items %v %s", err, itemResponse.Body.String())
	}
	startResponse := httptest.NewRecorder()
	serve.ServeHTTP(startResponse, httptest.NewRequest(http.MethodPost, "/api/v1/videos/batches/"+batch.ID+"/items/"+items.Items[0].ID+"/execute", bytes.NewBufferString(`{}`)))
	var started struct {
		Attempts []videogen.Attempt `json:"attempts"`
	}
	if err := json.NewDecoder(startResponse.Body).Decode(&started); err != nil || len(started.Attempts) != 1 {
		t.Fatalf("start %v %s", err, startResponse.Body.String())
	}
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "source.webm")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("video"))
	_ = writer.WriteField("media_type", "video/webm")
	_ = writer.Close()
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/v1/assets", &upload)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploadResponse := httptest.NewRecorder()
	serve.ServeHTTP(uploadResponse, uploadReq)
	var source struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(uploadResponse.Body).Decode(&source); err != nil || source.ID == "" {
		t.Fatalf("upload %v %s", err, uploadResponse.Body.String())
	}
	tailResponse := httptest.NewRecorder()
	serve.ServeHTTP(tailResponse, httptest.NewRequest(http.MethodPost, "/api/v1/videos/tail-extractions", bytes.NewBufferString(`{"source_asset_id":"`+source.ID+`","preset_id":"tail"}`)))
	var tail videogen.TailExtraction
	if err := json.NewDecoder(tailResponse.Body).Decode(&tail); err != nil || tailResponse.Code != 202 {
		t.Fatalf("tail %d %v %s", tailResponse.Code, err, tailResponse.Body.String())
	}
	var cliPID, tailPID int
	var lastAttempt videogen.Attempt
	var lastTail videogen.TailExtraction
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a := httptest.NewRecorder()
		serve.ServeHTTP(a, httptest.NewRequest(http.MethodGet, "/api/v1/videos/attempts/"+started.Attempts[0].ID, nil))
		var attempt videogen.Attempt
		_ = json.NewDecoder(a.Body).Decode(&attempt)
		lastAttempt = attempt
		b := httptest.NewRecorder()
		serve.ServeHTTP(b, httptest.NewRequest(http.MethodGet, "/api/v1/videos/tail-extractions/"+tail.ID, nil))
		var currentTail videogen.TailExtraction
		_ = json.NewDecoder(b.Body).Decode(&currentTail)
		lastTail = currentTail
		if attempt.PID > 0 && currentTail.PID > 0 {
			cliPID, tailPID = attempt.PID, currentTail.PID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cliPID == 0 || tailPID == 0 {
		t.Fatalf("PIDs cli=%d tail=%d attempt=%#v tail=%#v", cliPID, tailPID, lastAttempt, lastTail)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "video-workspace", "tail-"+tail.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "videos", "tail-workspaces")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old tail root: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.shutdownManagers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/videos/attempts/" + started.Attempts[0].ID, "/api/v1/videos/tail-extractions/" + tail.ID} {
		response := httptest.NewRecorder()
		serve.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"cancelled"`) {
			t.Fatalf("terminal %s = %d %s", path, response.Code, response.Body.String())
		}
	}
	for _, pid := range []int{cliPID, tailPID} {
		if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("process group %d survives: %v", pid, err)
		}
	}
}

func TestNewServerBoundsRequestReadsWithoutBoundingSSEWrites(t *testing.T) {
	server, err := NewServer(t.TempDir(), config.Default(), "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if server.ReadTimeout <= 0 {
		t.Fatalf("ReadTimeout = %v, want bounded JSON request reads", server.ReadTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want unbounded SSE writes", server.WriteTimeout)
	}
}

func TestNewServerRejectsInvalidPortOverride(t *testing.T) {
	for _, port := range []int{-1, 65536} {
		t.Run(fmt.Sprintf("port_%d", port), func(t *testing.T) {
			if _, err := NewServer(t.TempDir(), config.Default(), "test", port); err == nil {
				t.Fatalf("NewServer accepted port %d", port)
			}
		})
	}
}

func TestNewServerConnectsEmbeddedHandler(t *testing.T) {
	server, err := NewServer(t.TempDir(), config.Default(), "test-version", 0)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, "/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &responseRecorder{header: make(http.Header)}
	server.Handler.ServeHTTP(recorder, request)
	if recorder.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.status)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(recorder.body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != "test-version" {
		t.Fatalf("version = %q", body.Version)
	}
}

func TestRunStartsAndShutsDownWithContext(t *testing.T) {
	dataDir := t.TempDir()
	port := reservePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, Options{DataDir: dataDir, PortOverride: port, Version: "lifecycle-test"})
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", port)
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err := http.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("health status = %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	assetsResponse, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/assets", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = assetsResponse.Body.Close()
	if assetsResponse.StatusCode != http.StatusOK {
		t.Fatalf("asset list status = %d", assetsResponse.StatusCode)
	}
	knowledgeResponse, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/knowledge", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = knowledgeResponse.Body.Close()
	if knowledgeResponse.StatusCode != http.StatusOK {
		t.Fatalf("knowledge list status = %d", knowledgeResponse.StatusCode)
	}
	llmConfigResponse, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/llm/config", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = llmConfigResponse.Body.Close()
	if llmConfigResponse.StatusCode != http.StatusOK {
		t.Fatalf("LLM config status = %d", llmConfigResponse.StatusCode)
	}
	imageBatchResponse, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/images/batches", port),
		"application/json", bytes.NewReader([]byte(`{"title":"SSE lifecycle","provider_id":"sdcpp-local","concurrency":1,"base_params":{}}`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	var imageBatch struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(imageBatchResponse.Body).Decode(&imageBatch); err != nil {
		_ = imageBatchResponse.Body.Close()
		t.Fatal(err)
	}
	_ = imageBatchResponse.Body.Close()
	if imageBatchResponse.StatusCode != http.StatusCreated || imageBatch.ID == "" {
		t.Fatalf("create image batch = %d, %#v", imageBatchResponse.StatusCode, imageBatch)
	}
	imageEvents, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/images/batches/%s/events", port, imageBatch.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer imageEvents.Body.Close()
	if imageEvents.StatusCode != http.StatusOK || !strings.HasPrefix(imageEvents.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("image events status = %d, headers = %#v", imageEvents.StatusCode, imageEvents.Header)
	}
	response, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/llm/sessions", port),
		"application/json", bytes.NewReader([]byte(`{"title":"Lifecycle session","folder":"tests"}`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	var createdSession struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.NewDecoder(response.Body).Decode(&createdSession); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated || createdSession.Session.ID == "" {
		t.Fatalf("create LLM session = %d, %#v", response.StatusCode, createdSession)
	}

	profile := backend.DefaultProfile()
	profile.Name = "lifecycle backend"
	profile.Command = "sleep 30"
	encodedProfile, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.Post(fmt.Sprintf("http://127.0.0.1:%d/api/v1/backends", port), "application/json", bytes.NewReader(encodedProfile))
	if err != nil {
		t.Fatal(err)
	}
	var created backend.Profile
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create backend status = %d", response.StatusCode)
	}
	response, err = http.Post(fmt.Sprintf("http://127.0.0.1:%d/api/v1/backends/%s/start", port, created.ID), "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	var started backend.RunInfo
	if err := json.NewDecoder(response.Body).Decode(&started); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted || started.PID <= 0 {
		t.Fatalf("start backend = %d, %#v", response.StatusCode, started)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}

	info, err := os.Stat(filepath.Join(dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "instance.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "backends", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "assets", "index.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "knowledge", "notes.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "sessions", createdSession.Session.ID, "workspace.json")); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(filepath.Join(dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.SchemaVersion != config.CurrentSchemaVersion {
		t.Fatalf("config schema = %d, want %d", configuration.SchemaVersion, config.CurrentSchemaVersion)
	}
	if err := syscall.Kill(started.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("managed backend PID %d survived app shutdown: %v", started.PID, err)
	}
}

func TestRuntimeShutdownCancelsActiveLLMRun(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		select {
		case <-request.Context().Done():
		case <-releaseHandler:
		}
	}))
	defer providerServer.Close()
	defer close(releaseHandler)
	cfg := config.Default()
	cfg.LLM.Providers[0].URL = providerServer.URL
	runtime, err := newRuntime(t.TempDir(), cfg, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/llm/sessions", bytes.NewReader([]byte(`{"title":"shutdown"}`)))
	createResponse := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(createResponse, createRequest)
	var workspace struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Panels []struct {
			ID string `json:"id"`
		} `json:"panels"`
	}
	if err := json.NewDecoder(createResponse.Body).Decode(&workspace); err != nil || len(workspace.Panels) != 1 {
		t.Fatalf("workspace = %#v, error = %v", workspace, err)
	}
	executeBody := fmt.Sprintf(`{"panel_id":%q,"quick_path_ids":["local"]}`, workspace.Panels[0].ID)
	executeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/llm/sessions/"+workspace.Session.ID+"/execute", bytes.NewReader([]byte(executeBody)))
	executeResponse := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(executeResponse, executeRequest)
	var accepted struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(executeResponse.Body).Decode(&accepted); err != nil || len(accepted.Runs) != 1 {
		t.Fatalf("accepted = %#v, error = %v", accepted, err)
	}
	<-requestStarted
	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.shutdownManagers(shutdownContext); err != nil {
		t.Fatal(err)
	}
	run, exists := runtime.llmManager.Get(accepted.Runs[0].ID)
	if !exists || run.State != "cancelled" {
		t.Fatalf("run after shutdown = %#v, exists = %v", run, exists)
	}
}

func TestApplicationImageLifecyclePersistsBatchAndCancelsActiveAttempt(t *testing.T) {
	jobStarted := make(chan struct{})
	var startOnce sync.Once
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/sdcpp/v1/img_gen":
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(response, `{"id":"job-app","kind":"img_gen","status":"queued","created":1,"poll_url":"/sdcpp/v1/jobs/job-app"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/sdcpp/v1/jobs/job-app":
			startOnce.Do(func() { close(jobStarted) })
			<-request.Context().Done()
		case request.Method == http.MethodPost && request.URL.Path == "/sdcpp/v1/jobs/job-app/cancel":
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer provider.Close()
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.Images.Providers[0].BaseURL = provider.URL
	cfg.Images.Providers[0].PollIntervalMilliseconds = 100
	runtime, err := newRuntime(dataDir, cfg, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	configResponse := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(configResponse, httptest.NewRequest(http.MethodGet, "/api/v1/images/config", nil))
	if configResponse.Code != http.StatusOK || !strings.Contains(configResponse.Body.String(), `"id":"sdcpp-local"`) {
		t.Fatalf("config status = %d, body = %s", configResponse.Code, configResponse.Body.String())
	}
	create := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1/images/batches", bytes.NewReader([]byte(`{"title":"App image","provider_id":"sdcpp-local","concurrency":1,"base_params":{"output_format":"png"}}`))))
	var batch struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&batch); create.Code != http.StatusCreated || err != nil || batch.ID == "" {
		t.Fatalf("create status = %d, body = %s, error = %v", create.Code, create.Body.String(), err)
	}
	items := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(items, httptest.NewRequest(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/items", bytes.NewReader([]byte(`{"items":[{"prompt":"one"}]}`))))
	if items.Code != http.StatusCreated {
		t.Fatalf("items status = %d, body = %s", items.Code, items.Body.String())
	}
	execute := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(execute, httptest.NewRequest(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/execute", bytes.NewReader([]byte(`{}`))))
	var accepted struct {
		Attempts []struct {
			ID string `json:"id"`
		} `json:"attempts"`
	}
	if err := json.NewDecoder(execute.Body).Decode(&accepted); execute.Code != http.StatusAccepted || err != nil || len(accepted.Attempts) != 1 {
		t.Fatalf("execute status = %d, body = %s, error = %v", execute.Code, execute.Body.String(), err)
	}
	select {
	case <-jobStarted:
	case <-time.After(time.Second):
		t.Fatal("image job polling did not start")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.shutdownManagers(shutdownContext); err != nil {
		t.Fatal(err)
	}
	attempt, ok := runtime.imageManager.GetAttempt(accepted.Attempts[0].ID)
	if !ok || attempt.State != "cancelled" {
		t.Fatalf("attempt = %#v, exists = %v", attempt, ok)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "images", "batches", batch.ID, "batch.json")); err != nil {
		t.Fatal(err)
	}
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

type responseRecorder struct {
	header http.Header
	body   []byte
	status int
}

func (recorder *responseRecorder) Header() http.Header { return recorder.header }

func (recorder *responseRecorder) Write(body []byte) (int, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	recorder.body = append(recorder.body, body...)
	return len(body), nil
}

func (recorder *responseRecorder) WriteHeader(status int) { recorder.status = status }
