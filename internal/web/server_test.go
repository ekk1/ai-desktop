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

func TestEmbeddedBackendEditorExposesFoldedWorkerControls(t *testing.T) {
	index := getBody(t, "/")
	for _, marker := range []string{
		`id="backend-execution-kind"`,
		`id="backend-worker-url"`,
		`id="backend-worker-test"`,
		`id="backend-worker-test-result"`,
		`id="backend-execution-summary"`,
		`id="backend-worker-instance"`,
		`id="backend-worker-run"`,
		`id="backend-worker-connection"`,
	} {
		if !strings.Contains(index, marker) {
			t.Errorf("backend worker UI does not contain %s", marker)
		}
	}
	if !strings.Contains(getBody(t, "/assets/app.js"), `/api/v1/backends/worker/test`) {
		t.Error("backend UI does not call the worker connection endpoint")
	}
}

func TestEmbeddedLLMConfigExposesProgressiveProviderEditors(t *testing.T) {
	index := getBody(t, "/")
	for _, id := range []string{
		`id="settings-workspace"`,
		`id="llm-provider-list"`,
		`id="llm-provider-new"`,
		`id="llm-quick-path-list"`,
		`id="llm-quick-path-new"`,
		`id="llm-template-list"`,
		`id="llm-template-new"`,
		`id="llm-exa-key"`,
		`id="llm-preset-llama"`,
		`id="llm-config-save-status"`,
		`id="llm-provider-editor"`,
		`id="llm-quick-path-editor"`,
		`id="llm-template-editor"`,
	} {
		if !strings.Contains(index, id) {
			t.Errorf("LLM config does not contain %s", id)
		}
	}
	script := getBody(t, "/assets/llm-config.js")
	for _, endpoint := range []string{
		`fetch("/api/v1/llm/config"`,
		`/api/v1/llm/providers/preset/llama-completion`,
	} {
		if !strings.Contains(script, endpoint) {
			t.Errorf("LLM config script does not contain %s", endpoint)
		}
	}
}

func TestEmbeddedImageConfigExposesProgressiveProviderEditors(t *testing.T) {
	index := getBody(t, "/")
	for _, marker := range []string{
		`id="image-provider-list"`, `id="image-provider-new"`, `id="image-config-refresh"`,
		`id="image-config-save"`, `id="image-config-save-status"`, `id="image-provider-editor"`,
		`id="image-provider-id"`, `id="image-provider-name"`, `id="image-provider-base-url"`,
		`id="image-provider-enabled"`, `id="image-provider-concurrency"`, `id="image-provider-headers"`,
		`id="image-provider-connect-timeout"`, `id="image-provider-job-timeout"`, `id="image-provider-poll-interval"`,
		`id="image-provider-max-response"`, `id="image-provider-max-image"`, `id="image-provider-capability-status"`,
	} {
		if !strings.Contains(index, marker) {
			t.Errorf("Image config does not contain %s", marker)
		}
	}
	script := getBody(t, "/assets/image-config.js")
	for _, behavior := range []string{
		`export function createImageConfig`, `fetch("/api/v1/images/config"`, `/capabilities`, `Headers`,
	} {
		if !strings.Contains(script, behavior) {
			t.Errorf("Image config script does not contain %s", behavior)
		}
	}
	app := getBody(t, "/assets/app.js")
	if !strings.Contains(app, `createImageConfig`) {
		t.Error("app does not wire createImageConfig")
	}
}

func TestEmbeddedVideoConfigExposesProgressivePresetEditors(t *testing.T) {
	index := getBody(t, "/")
	for _, marker := range []string{
		`id="video-http-provider-list"`, `id="video-http-provider-new"`,
		`id="video-cli-preset-list"`, `id="video-cli-preset-new"`,
		`id="video-tail-frame-preset-list"`, `id="video-tail-frame-preset-new"`,
		`id="video-config-refresh"`, `id="video-config-save"`, `id="video-config-save-status"`,
		`id="video-http-provider-editor"`, `id="video-cli-preset-editor"`, `id="video-tail-frame-preset-editor"`,
		`id="video-http-provider-name"`, `id="video-http-provider-base-url"`, `id="video-http-provider-enabled"`, `id="video-http-provider-concurrency"`,
		`id="video-cli-preset-command"`, `id="video-cli-preset-work-dir"`, `id="video-cli-preset-output-path"`,
		`id="video-tail-frame-preset-command"`, `id="video-tail-frame-preset-extension"`,
		`<details class="advanced-form">`, `仅本机执行`, `_RAW`,
	} {
		if !strings.Contains(index, marker) {
			t.Errorf("video config UI does not contain %s", marker)
		}
	}
	videoMarkup := index[strings.Index(index, `id="video-http-provider-editor"`):]
	if strings.Contains(videoMarkup, "Remote Worker") || strings.Contains(videoMarkup, "远端 Worker") {
		t.Error("video config dialogs must not offer Remote Worker")
	}

	script := getBody(t, "/assets/video-config.js")
	for _, behavior := range []string{
		`export function createVideoConfig`, `fetch("/api/v1/videos/config"`, `method: "PUT"`,
		`/capabilities`, `video_generation_supported`, `JSON Object`, `clone(configuration)`,
	} {
		if !strings.Contains(script, behavior) {
			t.Errorf("video config script does not contain %s", behavior)
		}
	}
	if strings.Contains(script, "Remote Worker") || strings.Contains(script, "远端 Worker") {
		t.Error("video config script must not offer Remote Worker")
	}
	if !strings.Contains(script, `command_template: "extract-tail --output {{OUTPUT_IMAGE}}"`) {
		t.Error("tail-frame default must use the OUTPUT_IMAGE template token")
	}
	if strings.Contains(script, `command_template: "extract-tail --output {{OUTPUT_PATH}}"`) {
		t.Error("tail-frame default must not use the CLI OUTPUT_PATH template token")
	}
	if !strings.Contains(getBody(t, "/assets/app.js"), `createVideoConfig`) {
		t.Error("app does not wire createVideoConfig")
	}
}

func TestEmbeddedLLMWorkspaceExposesBranchingPanelControls(t *testing.T) {
	index := getBody(t, "/")
	for _, marker := range []string{
		`id="llm-workspace"`,
		`id="llm-session-new"`,
		`id="llm-session-folder-filter"`,
		`id="llm-session-list"`,
		`id="llm-session-title"`,
		`id="llm-session-folder"`,
		`id="llm-session-save"`,
		`id="llm-session-fork"`,
		`id="llm-current-path"`,
		`id="llm-branch-chooser"`,
		`data-panel-field="title"`,
		`data-panel-field="content"`,
		`data-panel-field="included"`,
		`data-panel-action="collapse"`,
		`data-panel-action="new-child"`,
		`data-panel-action="fork"`,
		`data-panel-action="delete"`,
		`data-panel-action="knowledge"`,
		`data-panel-action="assets"`,
		`data-panel-action="exa"`,
		`data-panel-details="revisions"`,
		`data-panel-details="technical"`,
		`id="llm-quick-path-bar"`,
		`id="llm-run-list"`,
		`data-run-action="cancel"`,
	} {
		if !strings.Contains(index, marker) {
			t.Errorf("LLM workspace does not contain %s", marker)
		}
	}
	script := getBody(t, "/assets/llm.js")
	for _, behavior := range []string{
		`export function createLLMWorkspace`,
		`/api/v1/llm/sessions`,
		`/execute`,
		`new EventSource`,
		`/exa`,
		`openAssetPicker`,
	} {
		if !strings.Contains(script, behavior) {
			t.Errorf("LLM workspace script does not contain %s", behavior)
		}
	}
	styles := getBody(t, "/assets/styles.css")
	for _, marker := range []string{
		`.llm-session-item`,
		`.llm-panel-card`,
		`.llm-execution-bar`,
		`.llm-run-card`,
		`.reference-picker-dialog`,
	} {
		if !strings.Contains(styles, marker) {
			t.Errorf("LLM workspace styles do not contain %s", marker)
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
	if strings.Contains(script, "/logs/clear") {
		t.Error("backend clear button still clears the server log mirror")
	}
	for _, behavior := range []string{"backendLogClearOffset", "offset"} {
		if !strings.Contains(script, behavior) {
			t.Errorf("backend script does not preserve cleared log offsets: missing %s", behavior)
		}
	}
	for _, behavior := range []string{"Uint8Array", "atob", "TextDecoder", "data_base64", "start_offset", "end_offset"} {
		if !strings.Contains(script, behavior) {
			t.Errorf("backend script does not preserve raw-byte log offsets: missing %s", behavior)
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

func TestEmbeddedImageWorkspaceExposesBatchItemAndResultWorkflow(t *testing.T) {
	index := getBody(t, "/")
	for _, id := range []string{
		`id="image-sidebar-controls"`, `id="image-batch-folder-filter"`, `id="image-batch-new"`, `id="image-batch-list"`,
		`id="image-workspace"`, `id="image-batch-title"`, `id="image-batch-folder"`, `id="image-batch-provider"`,
		`id="image-batch-concurrency"`, `id="image-batch-save"`, `id="image-batch-delete"`, `id="image-batch-capabilities"`,
		`id="image-param-width"`, `id="image-param-height"`, `id="image-param-seed"`, `id="image-param-batch-count"`,
		`id="image-param-steps"`, `id="image-param-cfg"`, `id="image-param-method"`, `id="image-param-scheduler"`,
		`id="image-param-format"`, `id="image-base-params-json"`, `id="image-bulk-prompts"`, `id="image-item-list"`,
		`id="image-item-editor"`, `id="image-item-prompt"`, `id="image-item-negative"`, `id="image-item-override"`,
		`id="image-item-init"`, `id="image-item-refs"`, `id="image-item-mask"`, `id="image-item-control"`,
		`id="image-item-ip-adapter"`, `id="image-item-up"`, `id="image-item-down"`, `id="image-item-copy"`,
		`id="image-item-delete"`, `id="image-item-run"`, `id="image-item-retry"`, `id="image-item-cancel"`,
		`id="image-result-grid"`, `id="image-attempt-history"`, `id="image-attempt-technical"`,
	} {
		if !strings.Contains(index, id) {
			t.Errorf("image workspace does not contain %s", id)
		}
	}

	script := getBody(t, "/assets/images.js")
	for _, behavior := range []string{
		`export function createImageWorkspace`, `/api/v1/images/batches`, `/execute`, `/move`,
		`new EventSource`, `openAssetPicker`, `/api/v1/assets/`, `/state`,
	} {
		if !strings.Contains(script, behavior) {
			t.Errorf("image workspace script does not contain %s", behavior)
		}
	}

	styles := getBody(t, "/assets/styles.css")
	for _, marker := range []string{`.image-workspace`, `.image-item-card`, `.image-result-grid`, `.image-asset-reference`} {
		if !strings.Contains(styles, marker) {
			t.Errorf("image workspace styles do not contain %s", marker)
		}
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
