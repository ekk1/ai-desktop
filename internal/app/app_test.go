package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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

	profile := backend.DefaultProfile()
	profile.Name = "lifecycle backend"
	profile.Command = "sleep 30"
	encodedProfile, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/api/v1/backends", port), "application/json", bytes.NewReader(encodedProfile))
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
	if err := syscall.Kill(started.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("managed backend PID %d survived app shutdown: %v", started.PID, err)
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
