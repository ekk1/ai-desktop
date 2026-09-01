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
		{path: "/assets/videos.js", contentType: "text/javascript"},
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

func TestEmbeddedWorkspaceNavigationKeepsHiddenViewsOutOfLayout(t *testing.T) {
	styles := getBody(t, "/assets/styles.css")
	if !strings.Contains(styles, "[hidden] {\n  display: none !important;\n}") {
		t.Fatal("workspace CSS does not force hidden views out of layout")
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
	if !strings.Contains(script, `command_template: "generate-video --output {{OUTPUT_PATH}}"`) {
		t.Error("CLI default must use the OUTPUT_PATH template token")
	}
	if strings.Contains(script, `command_template: "generate-video --output {{OUTPUT_IMAGE}}"`) {
		t.Error("CLI default must not use the tail-frame OUTPUT_IMAGE template token")
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

func TestEmbeddedVideoWorkspace(t *testing.T) {
	index := getBody(t, "/")
	for _, id := range []string{
		`id="video-sidebar-controls"`, `id="video-batch-folder-filter"`, `id="video-batch-new"`, `id="video-batch-list"`,
		`id="video-workspace"`, `id="video-batch-title"`, `id="video-batch-folder"`, `id="video-execution-kind"`,
		`id="video-batch-preset"`, `id="video-batch-save"`, `id="video-batch-run"`, `id="video-timing-frames"`,
		`id="video-timing-duration"`, `id="video-requested-frames"`, `id="video-common-params"`, `id="video-bulk-prompts"`,
		`id="video-item-list"`, `id="video-item-editor"`, `id="video-item-enabled"`, `id="video-item-init"`,
		`id="video-item-end"`, `id="video-item-control"`, `id="video-item-cli-assets"`, `id="video-cli-log"`,
		`id="video-item-up"`, `id="video-item-down"`, `id="video-item-copy"`, `id="video-item-delete"`,
		`id="video-item-run"`, `id="video-attempt-history"`, `id="video-result-list"`, `id="video-tail-preset"`,
	} {
		if !strings.Contains(index, id) {
			t.Errorf("video workspace does not contain %s", id)
		}
	}
	for _, disclosure := range []string{
		`<summary>请求快照、Job 与错误</summary>`,
		`<summary>CLI 日志与工作区路径</summary>`,
		`<summary>Attempt 历史</summary>`,
		`<summary>模型限制与高级参数</summary>`,
	} {
		if !strings.Contains(index, disclosure) {
			t.Errorf("video workspace does not progressively disclose %s", disclosure)
		}
	}
	if !strings.Contains(index, `<video data-video-result controls preload="metadata"></video>`) {
		t.Error("video results do not use a native metadata-preloaded controls element")
	}

	app := getBody(t, "/assets/app.js")
	for _, marker := range []string{`import { createVideoWorkspace } from "/assets/videos.js"`, `createVideoWorkspace({`, `videoWorkspaceController.enter()`, `videoWorkspaceController.leave()`} {
		if !strings.Contains(app, marker) {
			t.Errorf("app does not wire the video workspace lifecycle: missing %s", marker)
		}
	}

	script := getBody(t, "/assets/videos.js")
	for _, behavior := range []string{
		`export function createVideoWorkspace`, `/api/v1/videos/batches`, `/items`, `/move`, `/execute`,
		`/api/v1/videos/attempts/`, `/cancel`, `/logs`, `/logs/save`, `/workspace`,
		`/api/v1/videos/tail-extractions`, `/events`, `/api/v1/videos/config`,
		`openAssetPicker`, `mediaPrefix: "image/"`, `/api/v1/assets/`, `/content`, `/state`,
		`new EventSource`, `.close()`, `start_offset`, `end_offset`, `data_base64`, `atob(`, `clearOffset`,
		`Math.ceil`, `duration_seconds`, `video_frames`, `actual_frame_count`, `requested_frames`,
		`function leave()`, `closeBatchEvents();`, `closeAttemptLog();`, `closeTailEvents();`, `clearTimers();`,
		"video.src = `/api/v1/assets/${encodeURIComponent(result.assetID)}/content`;", `已归档但仍引用`,
	} {
		if !strings.Contains(script, behavior) {
			t.Errorf("video workspace script does not contain %s", behavior)
		}
	}
	if strings.Contains(script, `/logs/clear`) || strings.Contains(script, `/clear-log`) {
		t.Error("browser-only CLI log clearing must not call a clear endpoint")
	}
	if strings.Contains(script, `.src = asset.`) || strings.Contains(script, `workspace_relative_path}/content`) {
		t.Error("video previews must not use asset or workspace filesystem paths")
	}

	styles := getBody(t, "/assets/styles.css")
	for _, marker := range []string{
		`.video-workspace`, `.video-batch-list`, `.video-item-card`, `.video-result-card`,
		`@media (max-width: 720px)`, `.video-batch-toolbar`, `.video-item-layout`, `.video-result-card video`,
		`overflow-x: hidden`, `grid-template-columns: 1fr`, `width: 100%`,
	} {
		if !strings.Contains(styles, marker) {
			t.Errorf("video workspace styles do not contain %s", marker)
		}
	}
}

func TestEmbeddedVideoWorkspaceReviewFixContracts(t *testing.T) {
	index := getBody(t, "/")
	if !strings.Contains(index, `data-video-tail-history`) {
		t.Error("video result details do not expose ordered tail extraction history")
	}

	script := getBody(t, "/assets/videos.js")
	for _, behavior := range []string{
		`function reconcileSelectedCLIAssets(previous, pickerAssets)`,
		`function validLogSnapshot(startOffset, endOffset, byteLength, capacityBytes)`,
		`function invalidateLogStream(message)`,
		`logAwaitingSnapshot`, `scheduleLogReconnect`,
		`const tailExtractionsBySource = new Map()`,
		`function upsertTailExtraction(extraction)`,
		`function renderTailHistory(container, history)`,
		`function ensureAssetActive(assetID)`,
		`async function useTailAsNewItem(assetID)`,
		`async function loadBatchDetail(batchID)`,
		`function commitBatchSelection(batchID, loaded)`,
	} {
		if !strings.Contains(script, behavior) {
			t.Errorf("video review fix contract is missing %s", behavior)
		}
	}
	if !strings.Contains(script, "ui.cliPath.textContent = \"\";\n    ui.attemptHistory.replaceChildren();") {
		t.Error("attempt rendering does not clear the previous CLI workspace path first")
	}

	section := func(start, end string) string {
		t.Helper()
		startAt := strings.Index(script, start)
		if startAt < 0 {
			t.Fatalf("video review fix section is missing %s", start)
		}
		endAt := strings.Index(script[startAt+len(start):], end)
		if endAt < 0 {
			t.Fatalf("video review fix section %s has no %s boundary", start, end)
		}
		return script[startAt : startAt+len(start)+endAt]
	}

	reconcile := section("function reconcileSelectedCLIAssets", "async function chooseCLIAssets")
	for _, marker := range []string{"previous.filter", "pickedIDs.has(item.asset_id)", "pickerAssets.filter", "return [...surviving, ...appended]"} {
		if !strings.Contains(reconcile, marker) {
			t.Errorf("CLI Asset reconciliation does not preserve prior order and append new selections: missing %s", marker)
		}
	}

	snapshot := section("function receiveLogSnapshot", "function receiveLogChunk")
	validationAt := strings.Index(snapshot, "if (!validLogSnapshot")
	mutationAt := strings.Index(snapshot, "const retained = retainLogWindow")
	if validationAt < 0 || mutationAt < 0 || validationAt > mutationAt || !strings.Contains(snapshot, "invalidateLogStream") || !strings.Contains(snapshot, "return;") {
		t.Error("log snapshot must be validated and rejected before authoritative offsets are mutated")
	}
	chunk := section("function receiveLogChunk", "function connectAttemptLog")
	for _, marker := range []string{"if (logAwaitingSnapshot) return", "if (startOffset > logEndOffset)", "invalidateLogStream", "return;"} {
		if !strings.Contains(chunk, marker) {
			t.Errorf("log chunk gap handling does not wait for a new authoritative snapshot: missing %s", marker)
		}
	}

	tailHistory := section("function renderTailHistory", "function renderTailActions")
	for _, marker := range []string{"[...history].reverse()", "stateText(extraction.state)", "extraction.error?.message", "saveTailLog(extraction.id)"} {
		if !strings.Contains(tailHistory, marker) {
			t.Errorf("tail extraction history is incomplete: missing %s", marker)
		}
	}
	tailEvents := section("function connectRelevantTailEvents", "function closeTailEvents")
	for _, marker := range []string{"latestTailExtraction(sourceAssetID)", "!terminalAttemptStates.has(extraction.state)", "latestTailExtraction(extraction.source_asset_id)?.id !== extraction.id"} {
		if !strings.Contains(tailEvents, marker) {
			t.Errorf("tail SSE does not limit subscriptions to the newest active extraction: missing %s", marker)
		}
	}

	currentTail := section("async function useTailAsCurrentItem", "async function ensureAssetActive")
	if activateAt, draftAt := strings.Index(currentTail, "await ensureAssetActive(assetID)"), strings.Index(currentTail, "openItemEditor(item.id, assetID)"); activateAt < 0 || draftAt < 0 || activateAt > draftAt {
		t.Error("current-item tail flow does not activate the tail Asset before opening the named Item draft")
	}
	if strings.Contains(currentTail, `jsonOptions("PUT"`) {
		t.Error("current-item tail flow still silently updates the stale editor target")
	}
	activation := section("async function ensureAssetActive", "async function useTailAsNewItem")
	for _, marker := range []string{"/state`, jsonOptions(\"POST\", { state: \"active\" })", "assetCache.set(asset.id, asset)", "无法先将归档尾帧设为精选"} {
		if !strings.Contains(activation, marker) {
			t.Errorf("tail Asset activation is incomplete: missing %s", marker)
		}
	}
	newTail := section("async function useTailAsNewItem", "async function saveTailLog")
	if activateAt, draftAt := strings.Index(newTail, "await ensureAssetActive(assetID)"), strings.Index(newTail, `openItemEditor("", assetID)`); activateAt < 0 || draftAt < 0 || activateAt > draftAt {
		t.Error("new-item tail flow does not activate the tail Asset before opening the draft")
	}

	selection := section("async function selectBatch", "async function refreshDetail")
	loadAt, commitAt := strings.Index(selection, "await loadBatchDetail(batchID)"), strings.Index(selection, "commitBatchSelection(batchID, loaded)")
	if loadAt < 0 || commitAt < 0 || loadAt > commitAt || strings.Contains(selection[:commitAt], "selectedBatchID = batchID") {
		t.Error("Batch selection must load and validate candidate detail before committing its route ID")
	}
	editVersionAt := strings.Index(selection, "const editVersion = batchEditVersion")
	editGuardAt := strings.Index(selection, "batchEditVersion !== editVersion")
	if editVersionAt < 0 || editGuardAt < 0 || editVersionAt > loadAt || editGuardAt < loadAt || editGuardAt > commitAt {
		t.Error("Batch selection must capture the draft version before candidate GET and reject edits before commit")
	} else if !strings.Contains(selection[editGuardAt:commitAt], "请再次点击") || !strings.Contains(selection[editGuardAt:commitAt], "return;") {
		t.Error("edit-during-selection must preserve the draft and require an explicit second click")
	}

	enter := section("async function enter()", "function leave()")
	activeGuardAt := strings.Index(enter, "if (active) {")
	firstShellAt := strings.Index(enter, "showWorkspaceShell();")
	returnAt := strings.Index(enter, "return;")
	activateAt := strings.Index(enter, "active = true;")
	lastShellAt := strings.LastIndex(enter, "showWorkspaceShell();")
	if activeGuardAt < 0 || firstShellAt < activeGuardAt || returnAt < firstShellAt || activateAt < returnAt || lastShellAt <= activateAt {
		t.Error("active enter must restore the shell before returning, while initial enter restores it before loading")
	}
	shell := section("function showWorkspaceShell()", "async function enter()")
	for _, marker := range []string{
		"ui.sidebarControls.hidden = false", `document.querySelector("#sidebar-content").replaceChildren(ui.sidebarControls)`,
		"ui.sidebarSearch.disabled = false", `ui.sidebarSearch.placeholder = "搜索视频批次标题或文件夹"`, "ui.workspace.hidden = false",
	} {
		if !strings.Contains(shell, marker) {
			t.Errorf("video shell restoration is incomplete: missing %s", marker)
		}
	}

	appScript := getBody(t, "/assets/app.js")
	sidebarResetAt := strings.Index(appScript, "sidebarContent.replaceChildren();")
	videoEnterAt := strings.Index(appScript, "videoWorkspaceController.enter();")
	if sidebarResetAt < 0 || videoEnterAt < 0 || sidebarResetAt > videoEnterAt {
		t.Error("module selection contract must account for the shared sidebar reset before video enter")
	}
}

func TestEmbeddedVideoWorkspaceFinalFixCContracts(t *testing.T) {
	index := getBody(t, "/")
	for _, marker := range []string{
		`id="video-item-timing-inherit"`, `id="video-item-timing-frames"`, `id="video-item-timing-duration"`,
		`id="video-item-frame-count"`, `id="video-item-duration-seconds"`, `id="video-item-fps"`, `id="video-item-requested-frames"`,
		`data-video-tail-log`, `data-video-tail-log-clear`,
	} {
		if !strings.Contains(index, marker) {
			t.Errorf("final fix C markup is missing %s", marker)
		}
	}

	script := getBody(t, "/assets/videos.js")
	for _, marker := range []string{
		`function syncItemTimingControls()`, `function itemTimingOverride()`, `timing_override: itemTimingOverride()`,
		`const tailLogViews = new Map()`, `const tailLogClearOffsets = new Map()`, `function receiveTailLogSnapshot`,
		`function receiveTailLogChunk`, `function invalidateTailLogStream`, `function closeTailLogs()`,
		`/tail-extractions/${encodeURIComponent(view.id)}/logs`, `openItemEditor(item.id, assetID)`,
	} {
		if !strings.Contains(script, marker) {
			t.Errorf("final fix C script is missing %s", marker)
		}
	}
	if strings.Contains(script, `/tail-extractions/${encodeURIComponent(extractionID)}/logs/clear`) {
		t.Error("Tail browser-only clear must not call a server clear endpoint")
	}

	section := func(start, end string) string {
		t.Helper()
		startAt := strings.Index(script, start)
		if startAt < 0 {
			t.Fatalf("final fix C section is missing %s", start)
		}
		endAt := strings.Index(script[startAt+len(start):], end)
		if endAt < 0 {
			t.Fatalf("final fix C section %s has no %s boundary", start, end)
		}
		return script[startAt : startAt+len(start)+endAt]
	}
	strictBase64 := section("function base64Bytes", "function appendBytes")
	for _, marker := range []string{`typeof value !== "string"`, `value.length % 4`, `base64Pattern.test(value)`, `const binary = atob(value)`, `btoa(binary) !== value`} {
		if !strings.Contains(strictBase64, marker) {
			t.Errorf("raw log Base64 validation is not strict/canonical: missing %s", marker)
		}
	}
	if strings.Contains(strictBase64, `value || ""`) {
		t.Error("missing raw-log Base64 must not be coerced to an empty payload")
	}
	attemptLogConnect := section("function connectAttemptLog", "function closeAttemptLog")
	for _, marker := range []string{`receiveLogSnapshot(JSON.parse(event.data))`, `receiveLogChunk(JSON.parse(event.data))`, `catch (error) { invalidateLogStream`} {
		if !strings.Contains(attemptLogConnect, marker) {
			t.Errorf("CLI malformed Base64 does not reach stream invalidation: missing %s", marker)
		}
	}

	itemEditor := section("function openItemEditor", "function updateItemActions")
	populateAt, syncAt, showAt := strings.Index(itemEditor, "item?.timing_override"), strings.Index(itemEditor, "syncItemTimingControls()"), strings.Index(itemEditor, "ui.itemDialog.showModal()")
	if populateAt < 0 || syncAt < populateAt || showAt < syncAt {
		t.Error("Item editor must populate and synchronize timing_override before showing the dialog")
	}
	itemTiming := section("function itemTimingOverride", "function itemPayload")
	for _, marker := range []string{`return null`, `mode: "frames"`, `video_frames`, `mode: "duration"`, `duration_seconds`, `fps`, `Math.ceil`} {
		if !strings.Contains(itemTiming, marker) {
			t.Errorf("Item timing payload is incomplete: missing %s", marker)
		}
	}
	if strings.Contains(itemTiming, "requestedFrames > 100000") {
		t.Error("duration overrides must not apply the frames-mode 100000 cap to ceil(duration*fps)")
	}
	itemPayload := section("function itemPayload", "async function saveItem")
	if timingAt, returnAt := strings.Index(itemPayload, "timing_override: itemTimingOverride()"), strings.Index(itemPayload, "return {"); timingAt < 0 || returnAt < 0 || timingAt < returnAt {
		t.Error("browser Item payload must explicitly serialize null or a complete timing_override")
	}

	tailSnapshot := section("function receiveTailLogSnapshot", "function receiveTailLogChunk")
	validationAt, mutationAt := strings.Index(tailSnapshot, "if (!validLogSnapshot"), strings.Index(tailSnapshot, "const retained = retainLogWindow")
	if validationAt < 0 || mutationAt < 0 || validationAt > mutationAt || !strings.Contains(tailSnapshot[:mutationAt], "invalidateTailLogStream") {
		t.Error("Tail log snapshots must reject malformed absolute offsets before mutating the view")
	}
	tailChunk := section("function receiveTailLogChunk", "function connectTailLog")
	gapAt, mutationAt := strings.Index(tailChunk, "startOffset > view.endOffset"), strings.Index(tailChunk, "const retained = retainLogWindow")
	if gapAt < 0 || mutationAt < 0 || gapAt > mutationAt || !strings.Contains(tailChunk[:mutationAt], "invalidateTailLogStream") {
		t.Error("Tail log gaps must invalidate and reconnect before any byte mutation")
	}
	tailLogConnect := section("function connectTailLog", "function clearTailLogLocally")
	for _, marker := range []string{`receiveTailLogSnapshot(view, JSON.parse(event.data))`, `receiveTailLogChunk(view, JSON.parse(event.data))`, `catch (error) { invalidateTailLogStream`} {
		if !strings.Contains(tailLogConnect, marker) {
			t.Errorf("Tail malformed Base64 does not reach stream invalidation: missing %s", marker)
		}
	}
	tailClear := section("function clearTailLogLocally", "function renderTailHistory")
	clearAt, rememberAt, renderAt := strings.Index(tailClear, "view.clearOffset = view.endOffset"), strings.Index(tailClear, "tailLogClearOffsets.set"), strings.Index(tailClear, "renderTailLog(view)")
	if clearAt < 0 || rememberAt < clearAt || renderAt < rememberAt || strings.Contains(tailClear, "request(") {
		t.Error("Tail log clear must advance and remember only the browser offset before rerendering")
	}
	for name, source := range map[string]string{
		"CLI snapshot":  section("function receiveLogSnapshot", "function receiveLogChunk"),
		"Tail snapshot": section("function receiveTailLogSnapshot", "function receiveTailLogChunk"),
	} {
		if !strings.Contains(source, "capacity_bytes") || !strings.Contains(source, "retainLogWindow") {
			t.Errorf("%s must apply the authoritative retained capacity", name)
		}
	}
	for name, check := range map[string]struct {
		source string
		start  string
	}{
		"CLI chunk":  {section("function receiveLogChunk", "function connectAttemptLog"), "logStartOffset = retained.startOffset"},
		"Tail chunk": {section("function receiveTailLogChunk", "function connectTailLog"), "view.startOffset = retained.startOffset"},
	} {
		if !strings.Contains(check.source, "retainLogWindow") || !strings.Contains(check.source, check.start) {
			t.Errorf("%s must keep the bounded window and its absolute start offset", name)
		}
	}
	for name, source := range map[string]string{
		"CLI clear":  section("function clearAttemptLogLocally", "async function saveAttemptLog"),
		"Tail clear": tailClear,
	} {
		if !strings.Contains(source, "new Uint8Array()") {
			t.Errorf("%s must release browser-retained hidden bytes", name)
		}
	}
	renderResults := section("function renderResults", "async function setAssetState")
	closeAt, replaceAt := strings.Index(renderResults, "closeTailLogs()"), strings.Index(renderResults, "ui.resultList.replaceChildren()")
	if closeAt < 0 || replaceAt < 0 || closeAt > replaceAt {
		t.Error("Tail log EventSources and timers must close before result history is replaced")
	}
	leave := section("function leave()", "ui.batchForm.addEventListener")
	if !strings.Contains(leave, "closeTailLogs();") || !strings.Contains(leave, "clearTimers();") {
		t.Error("workspace leave must close Tail log EventSources and reconnect timers")
	}

	currentTarget := section("async function useTailAsCurrentItem", "async function ensureAssetActive")
	activateAt, draftAt := strings.Index(currentTarget, "await ensureAssetActive(assetID)"), strings.Index(currentTarget, "openItemEditor(item.id, assetID)")
	if !strings.Contains(currentTarget, "window.confirm") || !strings.Contains(currentTarget, "itemTargetName(item)") || activateAt < 0 || draftAt < activateAt {
		t.Error("Tail reuse must name and confirm its target, activate the Asset, then open that Item draft")
	}
	if strings.Contains(currentTarget, `jsonOptions("PUT"`) {
		t.Error("Tail reuse must not silently PUT the stale editingItemID target")
	}

	styles := getBody(t, "/assets/styles.css")
	for _, marker := range []string{`.video-item-timing-layout`, `.video-tail-log`, `.video-tail-log-status`} {
		if !strings.Contains(styles, marker) {
			t.Errorf("final fix C styles are missing %s", marker)
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
