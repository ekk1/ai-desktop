package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/config"
)

func testHandler() http.Handler {
	return NewHandler(Options{
		Version: "test",
		DataDir: "/tmp/workbench-test",
		Config:  config.Default(),
	})
}

func TestHealthReturnsVersionedStatus(t *testing.T) {
	recorder := httptest.NewRecorder()
	testHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var got struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Version != "test" {
		t.Fatalf("health = %#v", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestSettingsExposeRuntimeConfiguration(t *testing.T) {
	recorder := httptest.NewRecorder()
	testHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var got struct {
		DataDir                string `json:"data_dir"`
		ListenPort             int    `json:"listen_port"`
		ShutdownTimeoutSeconds int    `json:"shutdown_timeout_seconds"`
		MaxUploadBytes         int64  `json:"max_upload_bytes"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.DataDir != "/tmp/workbench-test" || got.ListenPort != 8188 || got.ShutdownTimeoutSeconds != 10 || got.MaxUploadBytes != 268435456 {
		t.Fatalf("settings = %#v", got)
	}
}

func TestUnknownAPIUsesJSONErrorEnvelope(t *testing.T) {
	recorder := httptest.NewRecorder()
	testHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
	var got struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != "not_found" || got.Error.Message != "resource not found" {
		t.Fatalf("error = %#v", got.Error)
	}
}

func TestHealthRejectsWrongMethodWithJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	testHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/health", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestEmbeddedFilesAreServedAtExplicitPaths(t *testing.T) {
	tests := []struct {
		path        string
		contentType string
	}{
		{path: "/", contentType: "text/html"},
		{path: "/assets/styles.css", contentType: "text/css"},
		{path: "/assets/app.js", contentType: "text/javascript"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			testHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, test.contentType) {
				t.Fatalf("Content-Type = %q, want prefix %q", got, test.contentType)
			}
		})
	}

	recorder := httptest.NewRecorder()
	testHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/unknown-browser-path", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown browser path status = %d, want 404", recorder.Code)
	}
}

func TestEmbeddedWorkbenchShellExposesModulesAndResponsiveControls(t *testing.T) {
	index := getBody(t, "/")
	for _, module := range []string{"llm", "images", "video", "backends", "gallery", "knowledge", "settings"} {
		want := `data-module="` + module + `"`
		if !strings.Contains(index, want) {
			t.Errorf("index does not expose %s", want)
		}
	}
	for _, want := range []string{`aria-controls="workspace-sidebar"`, `id="workspace-sidebar"`, `id="page-title"`} {
		if !strings.Contains(index, want) {
			t.Errorf("index does not contain %s", want)
		}
	}

	styles := getBody(t, "/assets/styles.css")
	if !strings.Contains(styles, "@media (max-width: 760px)") {
		t.Error("styles do not define the narrow-screen layout")
	}

	script := getBody(t, "/assets/app.js")
	for _, endpoint := range []string{`fetch("/api/v1/health")`, `fetch("/api/v1/settings")`} {
		if !strings.Contains(script, endpoint) {
			t.Errorf("app script does not request %s", endpoint)
		}
	}
}

func TestEmbeddedBackendWorkspaceExposesEditorActionsAndStreaming(t *testing.T) {
	index := getBody(t, "/")
	for _, id := range []string{
		`id="backend-list"`,
		`id="backend-editor"`,
		`id="backend-command"`,
		`id="backend-start"`,
		`id="backend-stop"`,
		`id="backend-copy"`,
		`id="backend-log"`,
		`id="backend-log-save"`,
		`id="backend-log-clear"`,
	} {
		if !strings.Contains(index, id) {
			t.Errorf("backend workspace does not contain %s", id)
		}
	}

	script := getBody(t, "/assets/app.js")
	for _, behavior := range []string{`fetch("/api/v1/backends")`, "new EventSource"} {
		if !strings.Contains(script, behavior) {
			t.Errorf("backend script does not contain %s", behavior)
		}
	}
}

func TestEmbeddedGalleryExposesAssetWorkflowAndReusablePicker(t *testing.T) {
	index := getBody(t, "/")
	for _, id := range []string{
		`id="gallery-workspace"`,
		`id="gallery-file-input"`,
		`id="gallery-filter"`,
		`id="gallery-search"`,
		`id="gallery-grid"`,
		`id="gallery-select-all"`,
		`id="gallery-activate"`,
		`id="gallery-archive"`,
		`id="gallery-export"`,
		`id="gallery-preview"`,
		`id="asset-picker"`,
		`id="asset-picker-grid"`,
		`id="asset-picker-confirm"`,
	} {
		if !strings.Contains(index, id) {
			t.Errorf("gallery does not contain %s", id)
		}
	}

	script := getBody(t, "/assets/app.js")
	for _, behavior := range []string{
		`/api/v1/assets?state=active`,
		`/api/v1/assets/state`,
		`/api/v1/assets/export`,
	} {
		if !strings.Contains(script, behavior) {
			t.Errorf("gallery script does not use %s", behavior)
		}
	}

	styles := getBody(t, "/assets/styles.css")
	if !strings.Contains(styles, ".asset-grid") {
		t.Error("gallery styles do not define the responsive asset grid")
	}
}

func TestEmbeddedKnowledgeWorkspaceExposesMemoEditor(t *testing.T) {
	index := getBody(t, "/")
	for _, id := range []string{
		`id="knowledge-workspace"`,
		`id="knowledge-search"`,
		`id="knowledge-folder-filter"`,
		`id="knowledge-list"`,
		`id="knowledge-new"`,
		`id="knowledge-form"`,
		`id="knowledge-title"`,
		`id="knowledge-folder"`,
		`id="knowledge-tags"`,
		`id="knowledge-content"`,
		`id="knowledge-assets"`,
		`id="knowledge-choose-assets"`,
		`id="knowledge-save"`,
		`id="knowledge-delete"`,
	} {
		if !strings.Contains(index, id) {
			t.Errorf("knowledge workspace does not contain %s", id)
		}
	}

	script := getBody(t, "/assets/app.js")
	for _, behavior := range []string{`fetch("/api/v1/knowledge`, "window.openAssetPicker"} {
		if !strings.Contains(script, behavior) {
			t.Errorf("knowledge script does not contain %s", behavior)
		}
	}
}

func getBody(t *testing.T, path string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	testHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", path, recorder.Code)
	}
	return recorder.Body.String()
}
