# LLM 完整请求工作区实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付无聊天角色语义的 Panel 分支工作区、完整请求快照、可配置 LLM HTTP/SSE Provider、快捷路径、流式 Run 和手动 Exa 搜索。

**Architecture:** `internal/session` 持久化独立 Workspace 和 Panel 树并同步 Asset 引用；`internal/provider` 管理配置校验、受控模板和一次 HTTP/SSE 请求；`internal/llm` 组装快照并编排持久化 Run；`internal/exa` 严格识别并执行用户确认的搜索。浏览器新增独立 `llm.js`，通过现有原生 Shell 和 `/api/v1/llm/` API 工作。

**Tech Stack:** Go 1.24 标准库、版本化原子 JSON、`net/http`、SSE、原生 HTML/CSS/JavaScript

**Spec:** `docs/superpowers/specs/2026-08-30-llm-workspace-design.md`

## Global Constraints

- 单个 Go 二进制；Go 和浏览器端都不引入第三方依赖、CDN、构建工具或 Provider SDK。
- Panel 只有普通内容、知识和 Asset 引用，不出现 user/assistant/system 或聊天消息类型。
- 所有 Provider 请求由工作台 Server 发出；浏览器不直接持有或发送第三方 API Key。
- Provider 模板只做受控变量替换和顶层 JSON Object 合并，不执行 Go/JS 模板代码。
- 执行必须先持久化不可变 Snapshot；多个 QuickPath 结果是当前 Panel 下互为兄弟的子 Panel。
- Exa 只有严格单 JSON Object 匹配时才可由用户点击执行，绝不自动请求。
- 其他模块选择 Asset 时只展示 active；已引用后归档的 Asset 保持引用直到用户明确移除。
- 外部 HTTP 具有大小限制、超时和取消；错误正文只保留有限长度。

---

### Task 1: LLM 配置模型、Schema 迁移与运行时 Repository

**Files:**
- Create: `internal/provider/config.go`
- Create: `internal/provider/config_test.go`
- Create: `internal/config/repository.go`
- Create: `internal/config/repository_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces: `provider.LLMConfig`, `Provider`, `QuickPath`, `PromptTemplate`, `ExaConfig`
- Produces: `provider.DefaultLLMConfig()`, `LLMConfig.Validate()` and `LLMConfig.AddLlamaCompletionPreset()`
- Produces: `config.OpenRepository(path string)`, `Snapshot() Config`, `UpdateLLM(provider.LLMConfig) (Config, error)`
- Changes: `config.CurrentSchemaVersion` from 1 to 2 and adds `Config.LLM provider.LLMConfig`

- [x] **Step 1: Write failing Provider validation tests**

```go
func TestDefaultLLMConfigContainsRunnableLocalPath(t *testing.T) {
    cfg := DefaultLLMConfig()
    if err := cfg.Validate(); err != nil { t.Fatal(err) }
    if cfg.Providers[0].URL != "http://127.0.0.1:8080/completion" { t.Fatal(cfg.Providers) }
    if cfg.QuickPaths[0].ProviderID != cfg.Providers[0].ID { t.Fatal(cfg.QuickPaths) }
}

func TestLLMConfigRejectsBrokenReferencesAndUnsafeHeaders(t *testing.T) {
    cfg := DefaultLLMConfig()
    cfg.QuickPaths[0].ProviderID = "missing"
    if err := cfg.Validate(); err == nil { t.Fatal("missing provider accepted") }
    cfg = DefaultLLMConfig()
    cfg.Providers[0].Headers["Authorization"] = "Bearer x\nInjected: yes"
    if err := cfg.Validate(); err == nil { t.Fatal("header newline accepted") }
}
```

- [x] **Step 2: Run Provider tests and verify RED**

Run: `go test ./internal/provider -run TestLLMConfig -count=1 -v`

Expected: FAIL because the package and types do not exist.

- [x] **Step 3: Implement exact config types and validation**

Use these JSON fields:

```go
type Provider struct {
    ID string `json:"id"`; Name string `json:"name"`; URL string `json:"url"`; Method string `json:"method"`
    APIKey string `json:"api_key,omitempty"`; Headers map[string]string `json:"headers"`; BodyTemplate string `json:"body_template"`
    ResponseMode string `json:"response_mode"`; ResponseContentPath string `json:"response_content_path,omitempty"`
    StreamContentPath string `json:"stream_content_path,omitempty"`; StreamDonePath string `json:"stream_done_path,omitempty"`
    ConnectTimeoutSeconds int `json:"connect_timeout_seconds"`; TotalTimeoutSeconds int `json:"total_timeout_seconds"`
    MaxResponseBytes int64 `json:"max_response_bytes"`; MaxAssetBytes int64 `json:"max_asset_bytes"`; Enabled bool `json:"enabled"`
}
type QuickPath struct {
    ID string `json:"id"`; Name string `json:"name"`; ProviderID string `json:"provider_id"`; Model string `json:"model"`
    Params json.RawMessage `json:"params"`
}
type PromptTemplate struct { ID string `json:"id"`; Name string `json:"name"`; Content string `json:"content"` }
type ExaConfig struct {
    APIURL string `json:"api_url"`; APIKey string `json:"api_key,omitempty"`
    TimeoutSeconds int `json:"timeout_seconds"`; MaxResponseBytes int64 `json:"max_response_bytes"`
}
type LLMConfig struct {
    Providers []Provider `json:"providers"`; QuickPaths []QuickPath `json:"quick_paths"`
    PromptTemplates []PromptTemplate `json:"prompt_templates"`; Exa ExaConfig `json:"exa"`
}
```

Require stable unique IDs, valid HTTP(S) URLs, method `POST`, response mode `json|sse_json`, positive bounded timeouts/sizes, one valid JSON Object BodyTemplate after replacing `_JSON` placeholders with `null`, QuickPath Params as one JSON Object, and existing Provider references. Default IDs are `llama-local` and `local`.

- [x] **Step 4: Write config migration and runtime update tests**

```go
func TestLoadMigratesSchemaOneAndPreservesRuntimeFields(t *testing.T) {
    path := filepath.Join(t.TempDir(), "config.json")
    old := `{"schema_version":1,"listen_port":9001,"shutdown_timeout_seconds":15,"max_upload_bytes":1048576}`
    if err := os.WriteFile(path, []byte(old), 0o600); err != nil { t.Fatal(err) }
    got, err := Load(path)
    if err != nil { t.Fatal(err) }
    if got.SchemaVersion != 2 || got.ListenPort != 9001 || got.LLM.QuickPaths[0].Name != "Local" { t.Fatal(got) }
}
func TestRepositoryUpdateLLMPersistsWithPrivatePermissions(t *testing.T) {
    path := filepath.Join(t.TempDir(), "config.json")
    repository, err := OpenRepository(path)
    if err != nil { t.Fatal(err) }
    llm := repository.Snapshot().LLM
    llm.Exa.APIKey = "exa-test"
    if _, err := repository.UpdateLLM(llm); err != nil { t.Fatal(err) }
    reopened, err := OpenRepository(path)
    if err != nil { t.Fatal(err) }
    if reopened.Snapshot().LLM.Exa.APIKey != "exa-test" { t.Fatal(reopened.Snapshot()) }
    info, _ := os.Stat(path)
    if info.Mode().Perm() != 0o600 { t.Fatalf("mode=%o", info.Mode().Perm()) }
}
```

- [x] **Step 5: Run config tests and verify RED**

Run: `go test ./internal/config -run 'Test(LoadMigrates|RepositoryUpdateLLM)' -count=1 -v`

- [x] **Step 6: Implement Schema 2 migration and config.Repository**

Migration from 1 copies listen/upload/shutdown values and assigns `provider.DefaultLLMConfig()`. `Repository` serializes updates with a mutex, returns deep copies through JSON-safe clone helpers, and uses existing `Save` for `0600` atomic writes.

- [x] **Step 7: Verify and commit Task 1**

Run: `gofmt -w internal/provider internal/config && go test ./internal/provider ./internal/config -count=1`

```bash
git add internal/provider internal/config
git commit -m "feat: add runtime LLM configuration"
```

### Task 2: Session Workspace、Panel 树与修订

**Files:**
- Create: `internal/session/model.go`
- Create: `internal/session/repository.go`
- Create: `internal/session/repository_test.go`

**Interfaces:**
- Produces: `session.Session`, `Panel`, `Revision`, `ResultMetadata`, `Workspace`, `Filter`
- Produces: `session.OpenRepository(root string)`
- Produces: `CreateSession(CreateSessionInput) (Workspace,error)`, `List(Filter) []Session`, `Get(string) (Workspace,bool)`, `UpdateSession(string,UpdateSessionInput) (Workspace,error)`
- Produces: `CreatePanel(string,CreatePanelInput) (Panel,error)`, `UpdatePanel(string,string,UpdatePanelInput) (Panel,error)`, `DeletePanel(string,string) error`, `RestoreRevision(string,string,string) (Panel,error)`, `ForkSession(string,ForkSessionInput) (Workspace,error)`, `PathTo(string,string) ([]Panel,error)`
- Produces internal rollback primitive: `restoreWorkspace(Workspace) error`

- [x] **Step 1: Write failing tree/path/branch tests**

```go
func TestRepositoryBuildsCurrentPathAndStableSiblingBranches(t *testing.T) {
    repo, _ := OpenRepository(t.TempDir())
    workspace, _ := repo.CreateSession(CreateSessionInput{Title: "Research", Folder: "work"})
    root := workspace.Panels[0]
    left, _ := repo.CreatePanel(workspace.Session.ID, CreatePanelInput{ParentID: root.ID, Title: "Left", Content: "a"})
    right, _ := repo.CreatePanel(workspace.Session.ID, CreatePanelInput{ParentID: root.ID, Title: "Right", Content: "b"})
    got, _ := repo.PathTo(workspace.Session.ID, right.ID)
    if len(got) != 2 || got[0].ID != root.ID || got[1].ID != right.ID { t.Fatal(got) }
    gotWorkspace, _ := repo.Get(workspace.Session.ID)
    siblings := make([]Panel, 0, 2)
    for _, panel := range gotWorkspace.Panels {
        if panel.ParentID == root.ID { siblings = append(siblings, panel) }
    }
    if len(siblings) != 2 || siblings[0].ID != left.ID || siblings[1].ID != right.ID { t.Fatal(siblings) }
}
```

Add separate tests for missing parents, cycles being impossible through create-only ParentID, cross-session IDs, and root deletion rejection.

- [x] **Step 2: Run tree tests and verify RED**

Run: `go test ./internal/session -run 'TestRepository(Builds|Rejects)' -count=1 -v`

- [x] **Step 3: Implement model and one-file-per-workspace Repository**

Use `<root>/<session-id>/workspace.json`, schema 1, program-generated 32-hex IDs, directory scanning on open, deep copies, and stable sorting. `CreateSession` creates an included empty root Panel and makes it current.

- [x] **Step 4: Write failing revision/delete/fork/persistence tests**

```go
func TestUpdateCreatesRestorableRevision(t *testing.T) {
    repo, _ := OpenRepository(t.TempDir())
    workspace, _ := repo.CreateSession(CreateSessionInput{Title:"S"})
    root := workspace.Panels[0]
    updated, _ := repo.UpdatePanel(workspace.Session.ID, root.ID, UpdatePanelInput{Title:"new", Content:"new", Included:true})
    if len(updated.Revisions) != 1 || updated.Revisions[0].Content != "" { t.Fatal(updated) }
    restored, _ := repo.RestoreRevision(workspace.Session.ID, root.ID, updated.Revisions[0].ID)
    if restored.Content != "" || len(restored.Revisions) != 2 { t.Fatal(restored) }
}
func TestDeletePanelRemovesWholeSubtreeButNotRoot(t *testing.T) {
    repo, _ := OpenRepository(t.TempDir())
    workspace, _ := repo.CreateSession(CreateSessionInput{Title:"S"})
    root := workspace.Panels[0]
    child, _ := repo.CreatePanel(workspace.Session.ID, CreatePanelInput{ParentID:root.ID, Title:"child"})
    grandchild, _ := repo.CreatePanel(workspace.Session.ID, CreatePanelInput{ParentID:child.ID, Title:"grandchild"})
    if err := repo.DeletePanel(workspace.Session.ID, child.ID); err != nil { t.Fatal(err) }
    got, _ := repo.Get(workspace.Session.ID)
    if panelExists(got.Panels, child.ID) || panelExists(got.Panels, grandchild.ID) { t.Fatal(got.Panels) }
    if err := repo.DeletePanel(workspace.Session.ID, root.ID); !errors.Is(err, ErrRootPanel) { t.Fatal(err) }
}
func TestForkSessionCopiesOnlyRootToChosenNodeWithFreshIDs(t *testing.T) {
    repo, _ := OpenRepository(t.TempDir())
    source, _ := repo.CreateSession(CreateSessionInput{Title:"source"})
    root := source.Panels[0]
    chosen, _ := repo.CreatePanel(source.Session.ID, CreatePanelInput{ParentID:root.ID, Title:"chosen"})
    _, _ = repo.CreatePanel(source.Session.ID, CreatePanelInput{ParentID:root.ID, Title:"sibling"})
    forked, _ := repo.ForkSession(source.Session.ID, ForkSessionInput{PanelID:chosen.ID, Title:"fork", Folder:"copies"})
    if len(forked.Panels) != 2 || forked.Panels[0].ID == root.ID || forked.Panels[1].ID == chosen.ID { t.Fatal(forked) }
}
```

- [x] **Step 5: Implement revisions, subtree deletion and fork**

Content updates snapshot title/content/included/knowledge IDs/asset IDs before replacement. Collapse/current selection updates do not create Revision. Fork remaps ParentIDs in path order and resets Result metadata.

- [x] **Step 6: Verify and commit Task 2**

Run: `gofmt -w internal/session && go test ./internal/session -count=1 -v`

```bash
git add internal/session
git commit -m "feat: add branching LLM sessions"
```

### Task 3: Session Asset 引用一致性 Service

**Files:**
- Create: `internal/session/service.go`
- Create: `internal/session/service_test.go`

**Interfaces:**
- Consumes: `session.Repository`, `asset.Repository`
- Produces: `session.NewService(repository, assets)` with mutation methods matching Repository and read methods `List`, `Get`, `PathTo`
- Reference identity: `asset.Reference{Module:"session_panel", RecordID:panel.ID}`

- [x] **Step 1: Write failing lifecycle and rollback tests**

```go
func TestServiceSynchronizesPanelAssetReferences(t *testing.T) {
    service, assets := newSessionServiceFixture(t)
    first := importAssetFixture(t, assets, "first")
    second := importAssetFixture(t, assets, "second")
    workspace, _ := service.CreateSession(CreateSessionInput{Title:"S"})
    root := workspace.Panels[0]
    panel, _ := service.CreatePanel(workspace.Session.ID, CreatePanelInput{ParentID:root.ID, Title:"P", AssetIDs:[]string{first.ID}})
    assertPanelReference(t, assets, first.ID, panel.ID, true)
    _, _ = service.UpdatePanel(workspace.Session.ID, panel.ID, UpdatePanelInput{Title:"P", Included:true, AssetIDs:[]string{second.ID}})
    assertPanelReference(t, assets, first.ID, panel.ID, false)
    assertPanelReference(t, assets, second.ID, panel.ID, true)
    if err := service.DeletePanel(workspace.Session.ID, panel.ID); err != nil { t.Fatal(err) }
    assertPanelReference(t, assets, second.ID, panel.ID, false)
}
func TestServiceRejectsUnknownAssetWithoutChangingWorkspace(t *testing.T) {
    service, _ := newSessionServiceFixture(t)
    workspace, _ := service.CreateSession(CreateSessionInput{Title:"S"})
    root := workspace.Panels[0]
    if _, err := service.UpdatePanel(workspace.Session.ID, root.ID, UpdatePanelInput{Title:"changed", Included:true, AssetIDs:[]string{"missing"}}); !errors.Is(err, ErrAssetNotFound) { t.Fatal(err) }
    unchanged, _ := service.Get(workspace.Session.ID)
    if unchanged.Panels[0].Title != root.Title || len(unchanged.Panels[0].AssetIDs) != 0 { t.Fatal(unchanged) }
}
```

- [x] **Step 2: Run Service tests and verify RED**

Run: `go test ./internal/session -run TestService -count=1 -v`

- [x] **Step 3: Implement serialized compensating mutations**

Validate every new Asset ID before Workspace writes. Add new references, save Workspace, then remove obsolete references; on failure restore the exact old Workspace and reverse completed reference changes. Delete/fork operate on all affected Panels under one Service mutex.

- [x] **Step 4: Verify and commit Task 3**

Run: `go test ./internal/session ./internal/asset -count=1`

```bash
git add internal/session
git commit -m "feat: protect LLM panel assets"
```

### Task 4: 受控 JSON 模板与完整请求组装器

**Files:**
- Create: `internal/provider/template.go`
- Create: `internal/provider/template_test.go`
- Create: `internal/llm/snapshot.go`
- Create: `internal/llm/assembler.go`
- Create: `internal/llm/assembler_test.go`

**Interfaces:**
- Produces: `provider.TemplateVariables`, `provider.PreparedRequest`, `provider.Render(Provider, QuickPath, TemplateVariables)`
- Produces: `llm.NewAssembler(knowledgeService, assetRepository)`
- Produces: `Assembler.Build(workspace, panelID, provider, quickPath) (provider.PreparedRequest, llm.Snapshot, error)`

- [x] **Step 1: Write failing JSON safety and merge tests**

```go
func TestRenderJSONEncodesContentAndMergesQuickParams(t *testing.T) {
    p := DefaultLLMConfig().Providers[0]
    p.BodyTemplate = `{"prompt":${CONTENT_JSON},"stream":true}`
    q := QuickPath{Model:"m", Params:json.RawMessage(`{"temperature":0.2,"stream":false}`)}
    got, _ := Render(p, q, TemplateVariables{Content:`quote " and newline\n`})
    var body map[string]any
    if err := json.Unmarshal(got.Body, &body); err != nil { t.Fatal(err) }
    if body["prompt"] != "quote \" and newline\\n" || body["temperature"] != 0.2 || body["stream"] != false { t.Fatal(body) }
}
func TestRenderRejectsPlaceholderInsideJSONStringAndHeaderNewline(t *testing.T) {
    p := DefaultLLMConfig().Providers[0]
    p.BodyTemplate = `{"prompt":"${CONTENT_JSON}"}`
    if _, err := Render(p, QuickPath{Params:json.RawMessage(`{}`)}, TemplateVariables{Content:"x"}); !errors.Is(err, ErrPlaceholderPosition) { t.Fatal(err) }
    p = DefaultLLMConfig().Providers[0]
    p.Headers = map[string]string{"X-Test":"${API_KEY}\nInjected"}
    if _, err := Render(p, QuickPath{Params:json.RawMessage(`{}`)}, TemplateVariables{}); !errors.Is(err, ErrInvalidHeader) { t.Fatal(err) }
}
```

- [x] **Step 2: Run template tests and verify RED**

Run: `go test ./internal/provider -run TestRender -count=1 -v`

- [x] **Step 3: Implement token-aware placeholder replacement**

Recognize only the exact variables from the design. Replace `_JSON` with `json.Marshal` output, `${API_KEY}` only in Header values, validate one top-level Object, shallow-merge Params, and re-encode. Produce both real Headers and redacted snapshot Headers.

- [x] **Step 4: Write failing assembler ordering/Data URL/snapshot tests**

```go
func TestAssemblerUsesCurrentIncludedPathAndKnowledgeOrder(t *testing.T) {
    assembler, workspace, providerConfig, path := newAssemblerFixture(t)
    prepared, snapshot, err := assembler.Build(workspace, path[len(path)-1].ID, providerConfig.Providers[0], providerConfig.QuickPaths[0])
    if err != nil { t.Fatal(err) }
    if snapshot.Content != "root\n\nKnowledge title\nKnowledge body\n\nleaf" { t.Fatalf("content=%q", snapshot.Content) }
    if !bytes.Contains(prepared.Body, []byte(`"prompt":"root\\n\\nKnowledge title`)) { t.Fatal(string(prepared.Body)) }
}
func TestAssemblerIncludesPanelAndKnowledgeImageAssetsAsDataURLs(t *testing.T) {
    assembler, workspace, providerConfig, path := newImageAssemblerFixture(t)
    _, snapshot, err := assembler.Build(workspace, path[len(path)-1].ID, providerConfig.Providers[0], providerConfig.QuickPaths[0])
    if err != nil { t.Fatal(err) }
    if len(snapshot.AssetDataURLs) != 2 || !strings.HasPrefix(snapshot.AssetDataURLs[0], "data:image/png;base64,") { t.Fatal(snapshot.AssetDataURLs) }
}
func TestAssemblerRejectsNonImageAndOversizedAssetsBeforeSnapshot(t *testing.T) {
    assembler, workspace, providerConfig, path := newTextAssetAssemblerFixture(t)
    if _, _, err := assembler.Build(workspace, path[len(path)-1].ID, providerConfig.Providers[0], providerConfig.QuickPaths[0]); !errors.Is(err, ErrUnsupportedAsset) { t.Fatal(err) }
    assembler, workspace, providerConfig, path = newImageAssemblerFixture(t)
    providerConfig.Providers[0].MaxAssetBytes = 1
    if _, _, err := assembler.Build(workspace, path[len(path)-1].ID, providerConfig.Providers[0], providerConfig.QuickPaths[0]); !errors.Is(err, ErrAssetLimit) { t.Fatal(err) }
}
```

- [x] **Step 5: Implement immutable Snapshot assembly**

Copy all Panel, knowledge and Asset metadata; join content with exactly two newlines; encode image content; reject missing records and unsupported media; render PreparedRequest; Snapshot stores final Body and only redacted Headers.

- [x] **Step 6: Verify and commit Task 4**

Run: `go test ./internal/provider ./internal/llm -count=1 -v`

```bash
git add internal/provider internal/llm
git commit -m "feat: assemble complete LLM requests"
```

### Task 5: 通用 JSON HTTP/SSE Executor

**Files:**
- Create: `internal/provider/executor.go`
- Create: `internal/provider/executor_test.go`
- Create: `internal/provider/sse.go`
- Create: `internal/provider/sse_test.go`

**Interfaces:**
- Produces: `provider.Executor.Execute(ctx, PreparedRequest, func(string)) (ExecutionResult, error)`
- Produces: `ExecutionResult{Content, StatusCode, ResponseExcerpt}` and typed limit/path/status errors

- [ ] **Step 1: Write failing synchronous JSON tests using httptest.Server**

```go
func TestExecutorPostsExactBodyAndExtractsConfiguredJSONPath(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost || r.Header.Get("X-Test") != "value" { t.Errorf("request=%s %#v", r.Method, r.Header) }
        body, _ := io.ReadAll(r.Body)
        if string(body) != `{"prompt":"hello"}` { t.Errorf("body=%s", body) }
        _, _ = io.WriteString(w, `{"choices":[{"text":"done"}]}`)
    }))
    defer server.Close()
    request := PreparedRequest{URL:server.URL, Method:"POST", Headers:map[string]string{"X-Test":"value"}, Body:[]byte(`{"prompt":"hello"}`), ResponseMode:"json", ResponseContentPath:"choices.0.text", TotalTimeout:time.Second, MaxResponseBytes:1024}
    result, err := (Executor{}).Execute(context.Background(), request, func(string){})
    if err != nil || result.Content != "done" { t.Fatalf("result=%#v err=%v", result, err) }
}
```

Add cases for non-2xx excerpt truncation, response limit, invalid path and context cancellation.

- [ ] **Step 2: Run JSON Executor tests and verify RED**

Run: `go test ./internal/provider -run TestExecutor -count=1 -v`

- [ ] **Step 3: Implement bounded cancellable JSON execution**

Use a per-request `http.Client` and `net.Dialer` from Provider timeouts, `io.LimitReader(max+1)`, exact JSON path traversal over `map[string]any` and `[]any`, and never log Headers.

- [ ] **Step 4: Write failing standard SSE tests**

```go
func TestExecutorStreamsLlamaSSEAndIgnoresPing(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        flusher := w.(http.Flusher)
        w.Header().Set("Content-Type", "text/event-stream")
        _, _ = io.WriteString(w, ": ping\n\ndata: {\"content\":\"hel\",\"stop\":false}\n\ndata: {\"content\":\"lo\",\"stop\":true}\n\n")
        flusher.Flush()
        <-time.After(2*time.Second)
    }))
    defer server.Close()
    request := PreparedRequest{URL:server.URL, Method:"POST", Body:[]byte(`{}`), ResponseMode:"sse_json", StreamContentPath:"content", StreamDonePath:"stop", TotalTimeout:3*time.Second, MaxResponseBytes:1024}
    var chunks []string
    result, err := (Executor{}).Execute(context.Background(), request, func(chunk string){ chunks = append(chunks, chunk) })
    if err != nil || result.Content != "hello" || !slices.Equal(chunks, []string{"hel","lo"}) { t.Fatalf("result=%#v chunks=%#v err=%v", result, chunks, err) }
}
func TestSSEJoinsMultipleDataLinesAndHandlesDone(t *testing.T) {
    input := "data: {\"content\":\"a\"}\ndata: \n\n" + "data: [DONE]\n\n"
    events, err := readSSE(strings.NewReader(input), 1024)
    if err != nil || len(events) != 2 || events[0] != "{\"content\":\"a\"}\n" || events[1] != "[DONE]" { t.Fatalf("events=%#v err=%v", events, err) }
}
```

- [ ] **Step 5: Implement SSE parser and incremental extraction**

Scan with an explicit maximum event line size, join data lines with newline, ignore comments/event/id/retry fields, accept `[DONE]`, enforce aggregate response size, and stop when configured boolean done path is true.

- [ ] **Step 6: Verify and commit Task 5**

Run: `go test ./internal/provider -count=1 -v`

```bash
git add internal/provider
git commit -m "feat: execute configurable LLM providers"
```

### Task 6: 持久化 Run Store 与异步 Manager

**Files:**
- Create: `internal/llm/run.go`
- Create: `internal/llm/store.go`
- Create: `internal/llm/store_test.go`
- Create: `internal/llm/manager.go`
- Create: `internal/llm/manager_test.go`

**Interfaces:**
- Produces: `llm.Run`, `RunState`, `RunStore`, `OpenRunStore(sessionsRoot string)`
- Produces: `llm.NewManager(configRepository, sessionService, assembler, executor, runStore)`
- Produces: `Start(sessionID, panelID string, quickPathIDs []string) ([]Run,error)`, `Get`, `Cancel`, `Subscribe`, `Shutdown`

- [ ] **Step 1: Write failing persistence/interruption tests**

```go
func TestRunStoreReopensActiveRunsAsInterrupted(t *testing.T) {
    root := t.TempDir()
    store, _ := OpenRunStore(root)
    original := Run{ID:"run-a", SessionID:"session-a", State:RunRunning, Snapshot:Snapshot{Content:"request"}}
    if err := store.Save(original); err != nil { t.Fatal(err) }
    reopened, _ := OpenRunStore(root)
    got, ok := reopened.Get("run-a")
    if !ok || got.State != RunInterrupted || got.Error.Code != "interrupted" { t.Fatal(got) }
}
```

- [ ] **Step 2: Implement one JSON file per Run**

Store under the owning Session `runs/<run-id>.json`, schema 1. Snapshot is written before state `running`. Return deep copies and stable newest-first lists.

- [ ] **Step 3: Write failing multi-path streaming Manager test**

```go
func TestManagerCreatesSiblingPanelsForMultipleQuickPaths(t *testing.T) {
    fixture := newManagerFixture(t, []string{"first response", "second response"})
    runs, err := fixture.Manager.Start(fixture.SessionID, fixture.PanelID, []string{"path-a","path-b"})
    if err != nil || len(runs) != 2 { t.Fatalf("runs=%#v err=%v", runs, err) }
    waitForTerminalRuns(t, fixture.Manager, runs)
    workspace, _ := fixture.Sessions.Get(fixture.SessionID)
    children := childPanels(workspace.Panels, fixture.PanelID)
    if len(children) != 2 || children[0].ParentID != fixture.PanelID || children[1].ParentID != fixture.PanelID { t.Fatal(children) }
    if children[0].Result.RunID == children[1].Result.RunID { t.Fatal(children) }
}
```

Add tests for snapshot-before-request, subscriber snapshot/chunk/state order, one-run cancellation, failed request without success Panel, and Shutdown waiting for goroutines.

- [ ] **Step 4: Implement Manager lifecycle**

Copy Provider/QuickPath from one config Snapshot before starting, reject duplicate/disabled paths, persist all queued Runs, launch one goroutine per Run, keep bounded in-memory output, and synchronize subscriber channels without blocking Provider reads.

- [ ] **Step 5: Verify race safety and commit Task 6**

Run: `go test -race ./internal/llm ./internal/session ./internal/provider -count=1`

```bash
git add internal/llm
git commit -m "feat: manage streaming LLM runs"
```

### Task 7: 严格 Exa 检测与手动执行

**Files:**
- Create: `internal/exa/request.go`
- Create: `internal/exa/request_test.go`
- Create: `internal/exa/client.go`
- Create: `internal/exa/client_test.go`
- Create: `internal/llm/exa.go`
- Create: `internal/llm/exa_test.go`

**Interfaces:**
- Produces: `exa.Detect(content string) (SearchRequest, bool)`
- Produces: `exa.Client.Search(ctx, provider.ExaConfig, SearchRequest) (json.RawMessage,error)`
- Produces: `llm.ExaService.Execute(ctx, sessionID, panelID string) (session.Panel,error)`

- [ ] **Step 1: Write failing strict detection table**

```go
func TestDetectAcceptsOnlyExactExaObject(t *testing.T) {
    tests := []struct{ input string; ok bool }{
        {`{"tool":"exa.search","arguments":{"query":"go","num_results":8}}`, true},
        {`prefix {"tool":"exa.search","arguments":{"query":"go"}}`, false},
        {`{"tool":"exa.search","arguments":{"query":"go","extra":1}}`, false},
        {`{"tool":"exa.search","arguments":{"query":"go","num_results":101}}`, false},
    }
    for _, test := range tests {
        _, ok := Detect(test.input)
        if ok != test.ok { t.Errorf("Detect(%q)=%v want %v", test.input, ok, test.ok) }
    }
    got, ok := Detect(`{"tool":"exa.search","arguments":{"query":"go"}}`)
    if !ok || got.NumResults != 10 { t.Fatalf("request=%#v ok=%v", got, ok) }
}
```

- [ ] **Step 2: Implement DisallowUnknownFields detection**

Decode exactly one object, require EOF after it, exact tool name, non-empty trimmed query and 1–100 results.

- [ ] **Step 3: Write failing official field mapping and Panel result tests**

Use `httptest.Server` to assert `x-api-key`, `query`, camelCase `numResults`, and `contents.text=true`; return literal JSON and assert `ExaService` creates one formatted child Panel only on 2xx.

- [ ] **Step 4: Implement bounded Exa Client and Service**

Use request Context, config timeout/max bytes, redact Key from errors, pretty-print validated response JSON, title result `Exa: <query>`, and store request summary in Result metadata. Do not create a Panel on any failure.

- [ ] **Step 5: Verify and commit Task 7**

Run: `go test ./internal/exa ./internal/llm -count=1 -v`

```bash
git add internal/exa internal/llm
git commit -m "feat: execute confirmed Exa searches"
```

### Task 8: LLM HTTP API 与应用生命周期装配

**Files:**
- Create: `internal/web/llm_config.go`
- Create: `internal/web/llm_config_test.go`
- Create: `internal/web/llm_session.go`
- Create: `internal/web/llm_session_test.go`
- Create: `internal/web/llm_run.go`
- Create: `internal/web/llm_run_test.go`
- Create: `internal/web/llm_exa.go`
- Create: `internal/web/llm_exa_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Produces every endpoint in Design §9 with existing Error Envelope conventions
- Changes app runtime to own both `backend.Manager` and `llm.Manager`, shutting both down on every exit path

- [ ] **Step 1: Write failing config and Session API tests**

Test full `GET/PUT /api/v1/llm/config`, llama preset insertion, Session CRUD/filter/fork, Panel CRUD/restore, server-computed `exa_candidate`, strict unknown fields, malformed IDs and unknown resources using real temp repositories.

- [ ] **Step 2: Run API tests and verify RED**

Run: `go test ./internal/web -run 'TestLLM(Config|Session|Panel)' -count=1 -v`

- [ ] **Step 3: Implement focused Handler files and shared strict decoder**

Reuse one `decodeStrictJSON(response,request,max,target)` helper from Asset/Knowledge handlers, preserving their behavior. Do not expose disk paths or mutable Repository pointers.

- [ ] **Step 4: Write failing Run SSE/cancel/Exa API tests**

Use a streaming `httptest.Server`; assert execute returns `202`, SSE emits snapshot/chunk/state JSON, cancel is idempotent for completed Run, and Exa only works on a detected Panel.

- [ ] **Step 5: Implement Run and Exa routes**

SSE uses `text/event-stream`, immediate flush, heartbeat comments, request Context unsubscribe, and no Provider secret. Execute accepts `{panel_id,quick_path_ids}` only.

- [ ] **Step 6: Write and implement app lifecycle tests**

Extend the real `Run` test to assert config Schema 2, sessions directory, LLM APIs and cancellation on shutdown. Replace the current tuple return with an unexported runtime struct so both managers are always reachable.

- [ ] **Step 7: Verify and commit Task 8**

Run: `go test -race ./internal/web ./internal/app ./internal/llm -count=1`

```bash
git add internal/web internal/app
git commit -m "feat: expose LLM workspace API"
```

### Task 9: 原生 Provider 配置界面

**Files:**
- Create: `internal/web/static/llm-config.js`
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/server.go`
- Modify: `internal/web/server_test.go`

**Interfaces:**
- Consumes: `GET/PUT /api/v1/llm/config`, llama preset endpoint
- Produces: Settings 模块中的 Provider、QuickPath、PromptTemplate 与 Exa 配置 UI

- [ ] **Step 1: Write failing embedded UI contract test**

Assert served HTML has Provider list/editor、QuickPath editor、PromptTemplate editor、Exa Key、llama preset、保存状态挂载点；assert `/assets/llm-config.js` is served and requests the LLM config API.

- [ ] **Step 2: Run contract test and verify RED**

Run: `go test ./internal/web -run TestEmbeddedLLMConfig -count=1 -v`

- [ ] **Step 3: Implement progressive-disclosure Settings UI**

Default rows show name/enabled/edit. URL/timeouts in basic Dialog; API Key、Headers、Body Template、response mode/path/limits in `<details>`. QuickPath validates Params JSON locally. Save sends one complete config object and renders server validation beside the form.

- [ ] **Step 4: Verify and commit Task 9**

Run: `go test ./internal/web -count=1 && git diff --check`

```bash
git add internal/web
git commit -m "feat: configure LLM providers in browser"
```

### Task 10: 原生 LLM 分支工作区

**Files:**
- Create: `internal/web/static/llm.js`
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/server.go`
- Modify: `internal/web/server_test.go`

**Interfaces:**
- Consumes: Session/Panel/Run/Exa APIs, Knowledge API, `window.openAssetPicker`
- Produces: responsive LLM session sidebar, current-path Panel editor, branch selector, templates/references, QuickPath execution bar and live Run cards

- [ ] **Step 1: Write failing LLM workspace contract test**

Assert served markup exposes session search/new/list, editable title/folder, derive-session action, current path, Panel title/content/included/collapse/new-child/delete/revisions, knowledge/Asset selectors, branch chooser, QuickPath bar, Run status/cancel, Exa action and technical details mount points.

- [ ] **Step 2: Run contract test and verify RED**

Run: `go test ./internal/web -run TestEmbeddedLLMWorkspace -count=1 -v`

- [ ] **Step 3: Implement module lifecycle without enlarging app.js domain logic**

Export `createLLMWorkspace({sidebarContent,sidebarSearch,readAPIError,openAssetPicker})` with `enter()` and `leave()`. `app.js` only imports it and calls lifecycle hooks from `selectModule`; all LLM state/render/fetch/SSE logic stays in `llm.js`.

- [ ] **Step 4: Implement current-path and branch editing**

Render only API `current_path`; branch buttons change selected Panel through Session update. Save title/folder and Panel edits explicitly. Knowledge selector loads memo list; Asset selector calls active-only picker. Template insertion never executes. Revision and run snapshot details stay collapsed.

- [ ] **Step 5: Implement multi-QuickPath streaming and Exa confirmation**

Selected QuickPaths POST one execute request; open one EventSource per Run; append chunks with `textContent`, close on terminal state, refresh Workspace to reveal sibling results. Show Exa button only from server detection metadata and require click; never parse-and-send automatically in browser.

- [ ] **Step 6: Implement responsive styling and keyboard-safe Dialogs**

Desktop uses existing left sidebar plus wide Panel column; ≤760px uses the sidebar drawer, single Panel column, wrapping execution bar and near-full-screen dialogs. Every drag-capable ordering operation also has up/down text buttons.

- [ ] **Step 7: Verify and commit Task 10**

Run: `go test ./internal/web ./internal/app -count=1 && git diff --check`

```bash
git add internal/web
git commit -m "feat: add branching LLM workspace"
```

### Task 11: 阶段文档、完整验证与推送

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-08-30-llm-workspace.md`
- Modify: `docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md`

**Interfaces:**
- Produces: 用户运行说明、Provider/模板变量说明、数据路径、Exa 手动执行说明和完成状态

- [ ] **Step 1: Update README with exact workflows**

Document `<data-dir>/sessions`, main config secrets, Local preset, custom JSON/SSE extraction paths, SSH-only trust boundary, Panel branching, active Asset selection, manual Exa and interrupted Run behavior.

- [ ] **Step 2: Run full verification**

Run: `gofmt -w cmd internal && go vet ./... && go test ./... -count=1`

Run: `go test -race ./internal/config ./internal/session ./internal/provider ./internal/llm ./internal/exa ./internal/web ./internal/app -count=1`

Run: `VERIFY_DIR=$(mktemp -d) && go build -o "$VERIFY_DIR/ai-workbench" ./cmd/ai-workbench && git diff --check`

- [ ] **Step 3: Run binary HTTP smoke test**

Start the built binary on an unused loopback port with a temporary data dir. Use `curl` to create a Session, read default Local config, load the LLM workspace asset, and verify shutdown leaves no active managed Run.

- [ ] **Step 4: Review diff and close documentation**

Check requirements line by line, update this plan and the master design to completed, then commit:

```bash
git add README.md docs
git commit -m "docs: record LLM workspace delivery"
```

- [ ] **Step 5: Merge, push and verify remote**

Fast-forward the approved feature branch into `main`, rerun `go test ./... -count=1`, `git push origin main`, then assert `HEAD == origin/main` and a clean worktree before removing the local feature worktree.
