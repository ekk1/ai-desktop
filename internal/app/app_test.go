package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/backend"
	"github.com/ekk1/ai-desktop/internal/config"
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
	if configuration.SchemaVersion != 2 {
		t.Fatalf("config schema = %d, want 2", configuration.SchemaVersion)
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
