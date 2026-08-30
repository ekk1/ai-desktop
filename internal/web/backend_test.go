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

	"github.com/ekk1/ai-desktop/internal/backend"
	"github.com/ekk1/ai-desktop/internal/config"
)

func TestBackendProfileCRUDAndLifecycle(t *testing.T) {
	handler, manager := newBackendHandler(t)
	profile := backend.DefaultProfile()
	profile.Name = "test server"
	profile.Command = "printf 'started'; sleep 30"

	created := requestProfile(t, handler, http.MethodPost, "/api/v1/backends", profile, http.StatusCreated)
	if created.ID == "" {
		t.Fatal("created profile has no ID")
	}

	recorder := doJSON(t, handler, http.MethodGet, "/api/v1/backends", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d", recorder.Code)
	}
	var listed struct {
		Profiles []backend.Profile `json:"profiles"`
		Runs     []backend.RunInfo `json:"runs"`
	}
	decodeBody(t, recorder, &listed)
	if len(listed.Profiles) != 1 || listed.Profiles[0].ID != created.ID {
		t.Fatalf("profiles = %#v", listed.Profiles)
	}

	created.Description = "updated"
	updated := requestProfile(t, handler, http.MethodPut, "/api/v1/backends/"+created.ID, created, http.StatusOK)
	if updated.Description != "updated" || updated.ID != created.ID {
		t.Fatalf("updated = %#v", updated)
	}

	start := doJSON(t, handler, http.MethodPost, "/api/v1/backends/"+created.ID+"/start", map[string]any{"variables": map[string]string{}})
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d: %s", start.Code, start.Body.String())
	}
	if duplicate := doJSON(t, handler, http.MethodPost, "/api/v1/backends/"+created.ID+"/start", map[string]any{}); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate start status = %d", duplicate.Code)
	}
	waitForBackendLog(t, manager, created.ID, "started")

	logs := doJSON(t, handler, http.MethodGet, "/api/v1/backends/"+created.ID+"/logs", nil)
	if logs.Code != http.StatusOK || !strings.Contains(logs.Body.String(), "started") {
		t.Fatalf("logs = %d %s", logs.Code, logs.Body.String())
	}

	stop := doJSON(t, handler, http.MethodPost, "/api/v1/backends/"+created.ID+"/stop", nil)
	if stop.Code != http.StatusOK {
		t.Fatalf("stop status = %d: %s", stop.Code, stop.Body.String())
	}
	deleted := doJSON(t, handler, http.MethodDelete, "/api/v1/backends/"+created.ID, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", deleted.Code, deleted.Body.String())
	}
}

func TestBackendAPIRejectsInvalidJSONAndUnknownProfile(t *testing.T) {
	handler, _ := newBackendHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/backends", strings.NewReader("{broken"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d", recorder.Code)
	}

	missing := doJSON(t, handler, http.MethodPost, "/api/v1/backends/missing/start", map[string]any{})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing profile status = %d", missing.Code)
	}
}

func TestBackendLogEventsSendSnapshotAsSSE(t *testing.T) {
	handler, manager, repository := newBackendComponents(t)
	profile := backend.DefaultProfile()
	profile.Name = "events"
	profile.Command = "printf 'snapshot text'; sleep 30"
	created, err := repository.Create(profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(created.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitForBackendLog(t, manager, created.ID, "snapshot text")

	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/backends/"+created.ID+"/logs/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}

	reader := bufio.NewReader(response.Body)
	event, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	data, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if event != "event: snapshot\n" || !strings.Contains(data, "snapshot text") {
		t.Fatalf("SSE = %q%q", event, data)
	}
}

func newBackendHandler(t *testing.T) (http.Handler, *backend.Manager) {
	t.Helper()
	handler, manager, _ := newBackendComponents(t)
	return handler, manager
}

func newBackendComponents(t *testing.T) (http.Handler, *backend.Manager, *backend.Repository) {
	t.Helper()
	root := t.TempDir()
	repository, err := backend.OpenRepository(filepath.Join(root, "backends", "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	manager := backend.NewManager(repository, filepath.Join(root, "backends", "crash-logs"))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	handler := NewHandler(Options{
		Version:           "test",
		DataDir:           root,
		Config:            config.Default(),
		BackendRepository: repository,
		BackendManager:    manager,
	})
	return handler, manager, repository
}

func requestProfile(t *testing.T, handler http.Handler, method, path string, profile backend.Profile, wantStatus int) backend.Profile {
	t.Helper()
	recorder := doJSON(t, handler, method, path, profile)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d: %s", method, path, recorder.Code, recorder.Body.String())
	}
	var result backend.Profile
	decodeBody(t, recorder, &result)
	return result
}

func doJSON(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, requestBody)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
}

func waitForBackendLog(t *testing.T, manager *backend.Manager, profileID, expected string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		contents, err := manager.LogSnapshot(profileID)
		if err == nil && strings.Contains(string(contents), expected) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("log did not contain %q: %q, %v", expected, contents, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
