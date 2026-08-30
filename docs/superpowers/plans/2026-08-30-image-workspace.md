# 生图批次工作区实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付基于 stable-diffusion.cpp 原生异步 API 的可持久化生图批次、完整参数覆盖、Asset 输入与结果复用、有限并发执行和响应式原生界面。

**Architecture:** `internal/sdcpp` 定义图像 Provider、递归参数合并与受限 HTTP 协议；`internal/imagegen` 持久化 Batch/Item/Attempt，维护 Asset 引用并调度远端 Job；`internal/web` 暴露严格 JSON/SSE API。浏览器用独立 `image-config.js` 和 `images.js` 接入现有 Shell，复杂参数始终保留为 JSON Object。

**Tech Stack:** Go 1.24 标准库、版本化原子 JSON、`net/http`、SSE、原生 HTML/CSS/JavaScript

**Spec:** `docs/superpowers/specs/2026-08-30-image-workspace-design.md`

## Global Constraints

- 单个 Linux Go 二进制；Go 和浏览器端都不引入第三方依赖、CDN、包管理器或构建工具。
- 首版只执行 stable-diffusion.cpp 官方 `/sdcpp/v1/` 原生异步图像协议，不混入视频或兼容 API。
- Provider 请求只由工作台 Server 发出；浏览器不直连 stable-diffusion.cpp。
- Prompt、Negative Prompt 和五类图像字段由 Item/Asset 引用管理；其余当前和未来原生字段均可通过 JSON Object 表达。
- Attempt 必须在外部 HTTP 前持久化不可变 Snapshot；Snapshot 不保存 Base64/Data URL。
- 输入 Asset 选择只展示 active；归档后保留引用，直到用户明确移除。
- 结果逐张导入为 archive Asset；失败不登记伪成功结果，部分已导入结果保留。
- 外部 HTTP、响应正文、输入图片总量和单张输出都有硬上限；关闭和重启不自动重发远端 Job。

---

### Task 1: 图像 Provider 配置、Schema 3 迁移和运行时 Repository

**Files:**
- Create: `internal/sdcpp/config.go`
- Create: `internal/sdcpp/config_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/repository.go`
- Modify: `internal/config/repository_test.go`

**Interfaces:**
- Produces: `sdcpp.ImageProvider`, `ImageConfig`, `DefaultImageConfig()`, `DefaultImageParams()`, `ImageConfig.Validate()`, `ImageConfig.Clone()`
- Changes: `config.CurrentSchemaVersion` from 2 to 3 and adds `Config.Images sdcpp.ImageConfig`
- Produces: `(*config.Repository).UpdateImages(sdcpp.ImageConfig) (config.Config,error)`

- [x] **Step 1: Write failing Provider config tests**

```go
func TestDefaultImageConfigContainsRunnableLocalProvider(t *testing.T) {
    cfg := DefaultImageConfig()
    if err := cfg.Validate(); err != nil { t.Fatal(err) }
    got := cfg.Providers[0]
    if got.ID != "sdcpp-local" || got.BaseURL != "http://127.0.0.1:1234" || got.MaxConcurrentJobs != 1 {
        t.Fatal(got)
    }
}

func TestImageConfigRejectsInvalidLimitsAndHeaderInjection(t *testing.T) {
    cfg := DefaultImageConfig()
    cfg.Providers[0].Headers["X-Test"] = "ok\nInjected: yes"
    if err := cfg.Validate(); err == nil { t.Fatal("header newline accepted") }
    cfg = DefaultImageConfig()
    cfg.Providers[0].PollIntervalMilliseconds = 99
    if err := cfg.Validate(); err == nil { t.Fatal("short polling interval accepted") }
}
```

- [x] **Step 2: Run sdcpp config tests and verify RED**

Run: `go test ./internal/sdcpp -run 'Test(DefaultImage|ImageConfig)' -count=1 -v`

Expected: FAIL because `internal/sdcpp` does not exist.

- [x] **Step 3: Implement exact config model and validation**

```go
type ImageProvider struct {
    ID string `json:"id"`; Name string `json:"name"`; BaseURL string `json:"base_url"`
    Headers map[string]string `json:"headers"`
    ConnectTimeoutSeconds int `json:"connect_timeout_seconds"`; JobTimeoutSeconds int `json:"job_timeout_seconds"`
    PollIntervalMilliseconds int `json:"poll_interval_milliseconds"`
    MaxResponseBytes int64 `json:"max_response_bytes"`; MaxImageBytes int64 `json:"max_image_bytes"`
    MaxConcurrentJobs int `json:"max_concurrent_jobs"`; Enabled bool `json:"enabled"`
}
type ImageConfig struct { Providers []ImageProvider `json:"providers"` }
```

Require unique safe IDs, non-empty names, absolute HTTP(S) BaseURL without query/fragment, safe headers, connect 1–300 seconds, job 1–86400 seconds, polling 100–10000 ms, byte limits 1–1 GiB and concurrency 1–16. Default limits are 10 seconds, 3600 seconds, 750 ms, 256 MiB response, 128 MiB single image and concurrency 1. Normalize trailing `/` before validation in default and UI input; validation rejects a non-canonical trailing `/`.

`DefaultImageParams()` returns `{"width":1024,"height":1024,"seed":-1,"batch_count":1,"output_format":"png"}` as a fresh `json.RawMessage` copy. Service/API use it only when a new Batch omits BaseParams; existing empty Objects stay empty.

- [x] **Step 4: Write failing Schema 2 migration and repository tests**

```go
func TestLoadMigratesSchemaTwoWithImageDefaults(t *testing.T) {
    path := filepath.Join(t.TempDir(), "config.json")
    old := Default()
    old.SchemaVersion = 2
    old.Images = sdcpp.ImageConfig{}
    contents, _ := json.Marshal(old)
    if err := os.WriteFile(path, contents, 0o600); err != nil { t.Fatal(err) }
    got, err := Load(path)
    if err != nil { t.Fatal(err) }
    if got.SchemaVersion != 3 || got.Images.Providers[0].ID != "sdcpp-local" { t.Fatal(got) }
}

func TestRepositoryUpdateImagesPersistsDeepCopy(t *testing.T) {
    path := filepath.Join(t.TempDir(), "config.json")
    repository, _ := OpenRepository(path)
    images := repository.Snapshot().Images
    images.Providers[0].Name = "GPU Image"
    if _, err := repository.UpdateImages(images); err != nil { t.Fatal(err) }
    images.Providers[0].Headers["X-Late"] = "mutation"
    reopened, _ := OpenRepository(path)
    if reopened.Snapshot().Images.Providers[0].Headers["X-Late"] != "" { t.Fatal("alias retained") }
}
```

- [x] **Step 5: Run migration/repository tests and verify RED**

Run: `go test ./internal/config -run 'Test(LoadMigratesSchemaTwo|RepositoryUpdateImages)' -count=1 -v`

- [x] **Step 6: Implement Schema 3 and image config repository updates**

Migration 2→3 assigns `sdcpp.DefaultImageConfig()` while preserving every existing field. `Config.Clone` deep-clones Images; `Validate` delegates to `Images.Validate`. `UpdateImages` validates a cloned value while holding the existing repository mutex and writes through `config.Save`.

- [x] **Step 7: Verify and commit Task 1**

Run: `gofmt -w internal/sdcpp internal/config && go test ./internal/sdcpp ./internal/config -count=1 && git diff --check`

```bash
git add internal/sdcpp internal/config
git commit -m "feat: configure image providers"
```

### Task 2: Batch、Item、Attempt 模型和持久化 Repository

**Files:**
- Create: `internal/imagegen/model.go`
- Create: `internal/imagegen/repository.go`
- Create: `internal/imagegen/repository_test.go`

**Interfaces:**
- Produces: `Batch`, `Item`, `InputAssets`, `Attempt`, `AttemptState`, `Snapshot`, `AssetSnapshot`
- Produces: `OpenRepository(root string) (*Repository,error)` and Batch/Item/Attempt mutation methods below
- Consumes: Provider ID as a stable string; Repository does not import config or sdcpp

- [x] **Step 1: Write failing Batch and Item CRUD/order tests**

```go
func TestRepositoryPersistsOrderedBatchItems(t *testing.T) {
    root := t.TempDir()
    repository, _ := OpenRepository(root)
    batch, _ := repository.CreateBatch(CreateBatchInput{Title:"Draw", Folder:"ideas", ProviderID:"sdcpp-local", Concurrency:1, BaseParams:json.RawMessage(`{"width":768}`)})
    items, _ := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt:"one"},{Prompt:"two"}})
    if _, err := repository.MoveItem(batch.ID, items[1].ID, -1); err != nil { t.Fatal(err) }
    got, _ := repository.Get(batch.ID)
    if got.Items[0].Prompt != "two" || got.Items[0].Order != 0 || got.Items[1].Order != 1 { t.Fatal(got.Items) }
    reopened, _ := OpenRepository(root)
    persisted, _ := reopened.Get(batch.ID)
    if !reflect.DeepEqual(got, persisted) { t.Fatalf("reopened=%#v", persisted) }
}
```

Add table-driven `TestRepositoryRejectsInvalidBatchAndItemInputs` covering empty title, missing Provider ID, concurrency 0/17, non-Object JSON and every reserved managed key. Add `TestRepositoryRejectsMissingItemAndOutOfBoundsMove` asserting `ErrItemNotFound` and `ErrMoveBoundary` without mutating the stored Batch.

- [x] **Step 2: Run repository CRUD tests and verify RED**

Run: `go test ./internal/imagegen -run 'TestRepository(Persists|Rejects)' -count=1 -v`

Expected: FAIL because package/types do not exist.

- [x] **Step 3: Implement exact model and one-file-per-Batch Repository**

```go
type InputAssets struct {
    InitImageID string `json:"init_image_id,omitempty"`; RefImageIDs []string `json:"ref_image_ids"`
    MaskImageID string `json:"mask_image_id,omitempty"`; ControlImageID string `json:"control_image_id,omitempty"`
    IPAdapterImageID string `json:"ip_adapter_image_id,omitempty"`
}
type Batch struct {
    ID string `json:"id"`; Title string `json:"title"`; Folder string `json:"folder"`; ProviderID string `json:"provider_id"`
    Concurrency int `json:"concurrency"`; BaseParams json.RawMessage `json:"base_params"`; Items []Item `json:"items"`
    CreatedAt time.Time `json:"created_at"`; UpdatedAt time.Time `json:"updated_at"`
}
type Item struct {
    ID string `json:"id"`; Order int `json:"order"`; Prompt string `json:"prompt"`; NegativePrompt string `json:"negative_prompt"`
    ParamsOverride json.RawMessage `json:"params_override"`; InputAssets InputAssets `json:"input_assets"`; Attempts []Attempt `json:"attempts"`
    CreatedAt time.Time `json:"created_at"`; UpdatedAt time.Time `json:"updated_at"`
}
type Snapshot struct {
    Provider sdcpp.ImageProvider `json:"provider"`; Params json.RawMessage `json:"params"`
    Prompt string `json:"prompt"`; NegativePrompt string `json:"negative_prompt"`
    InputAssets []AssetSnapshot `json:"input_assets"`; CreatedAt time.Time `json:"created_at"`
}
type AssetSnapshot struct {
    ID string `json:"id"`; SHA256 string `json:"sha256"`; MediaType string `json:"media_type"`
    DisplayName string `json:"display_name"`; Size int64 `json:"size"`; Width int `json:"width,omitempty"`; Height int `json:"height,omitempty"`
}
type Attempt struct {
    ID string `json:"id"`; State AttemptState `json:"state"`; Snapshot Snapshot `json:"snapshot"`
    RemoteJobID string `json:"remote_job_id,omitempty"`; RemoteStatus string `json:"remote_status,omitempty"`; QueuePosition int `json:"queue_position,omitempty"`
    ResultAssetIDs []string `json:"result_asset_ids"`; Error AttemptError `json:"error"`
    CreatedAt time.Time `json:"created_at"`; StartedAt time.Time `json:"started_at,omitempty"`; CompletedAt time.Time `json:"completed_at,omitempty"`
}
type AttemptError struct { Code string `json:"code,omitempty"`; Message string `json:"message,omitempty"` }
type AttemptState string
const (
    AttemptQueued AttemptState = "queued"; AttemptSubmitting AttemptState = "submitting"; AttemptPolling AttemptState = "polling"
    AttemptSucceeded AttemptState = "succeeded"; AttemptFailed AttemptState = "failed"; AttemptCancelled AttemptState = "cancelled"; AttemptInterrupted AttemptState = "interrupted"
)
```

Persist `<root>/<32-hex-id>/batch.json` with schema 1, 0600 atomic JSON. Repository scans only safe 32-hex directories, validates stored identity/state, returns deep copies and stable newest-first Batch lists.

Expose:

```go
CreateBatch(CreateBatchInput) (Batch,error); List(Filter) []Batch; Get(string) (Batch,bool)
UpdateBatch(string,UpdateBatchInput) (Batch,error); DeleteBatch(string) error
CreateItems(string,[]CreateItemInput) ([]Item,error); UpdateItem(string,string,UpdateItemInput) (Item,error)
DeleteItem(string,string) error; MoveItem(string,string,int) (Batch,error)
CreateAttempt(string,string,CreateAttemptInput) (Attempt,error)
UpdateAttempt(string,string,string,UpdateAttemptInput) (Attempt,error)
```

`CreateAttemptInput` contains State and Snapshot. `UpdateAttemptInput` contains State, RemoteJobID, RemoteStatus, QueuePosition, ResultAssetIDs and AttemptError; Repository owns all timestamps and ignores no fields silently.

- [x] **Step 4: Write failing Attempt history and interrupted recovery tests**

```go
func TestRepositoryKeepsAttemptHistoryAndInterruptsActiveOnReopen(t *testing.T) {
    root := t.TempDir()
    repository, _ := OpenRepository(root)
    batch, _ := repository.CreateBatch(validBatchInput())
    item, _ := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt:"cat"}})
    first, _ := repository.CreateAttempt(batch.ID, item[0].ID, CreateAttemptInput{State:AttemptQueued, Snapshot:validSnapshot()})
    _, _ = repository.UpdateAttempt(batch.ID, item[0].ID, first.ID, UpdateAttemptInput{State:AttemptPolling, RemoteJobID:"job-a"})
    reopened, _ := OpenRepository(root)
    got, _ := reopened.Get(batch.ID)
    if len(got.Items[0].Attempts) != 1 || got.Items[0].Attempts[0].State != AttemptInterrupted { t.Fatal(got) }
    second, _ := reopened.CreateAttempt(batch.ID, item[0].ID, CreateAttemptInput{State:AttemptQueued, Snapshot:validSnapshot()})
    if second.ID == first.ID { t.Fatal("attempt ID reused") }
}
```

- [x] **Step 5: Run Attempt recovery tests and verify RED**

Run: `go test ./internal/imagegen -run TestRepositoryKeepsAttemptHistory -count=1 -v`

- [x] **Step 6: Implement Attempt transitions and reopen recovery**

Allowed forward transitions are queued→submitting/cancelled, submitting→polling/failed/cancelled, polling→succeeded/failed/cancelled, and any active state→interrupted during open. Terminal states never transition. ResultAssetIDs are unique; Error message is bounded to 4096 bytes; Snapshot and JSON fields deep-copy.

- [x] **Step 7: Verify and commit Task 2**

Run: `gofmt -w internal/imagegen && go test ./internal/imagegen -count=1 -v && git diff --check`

```bash
git add internal/imagegen
git commit -m "feat: persist image generation batches"
```

### Task 3: Batch Asset 引用一致性 Service

**Files:**
- Create: `internal/imagegen/service.go`
- Create: `internal/imagegen/service_test.go`

**Interfaces:**
- Consumes: `*imagegen.Repository`, `*asset.Repository`
- Produces: `NewService(repository,assets) *Service` with Batch/Item read/mutation wrappers
- Produces Manager primitives: `CreateAttempt`, `UpdateAttempt`, `AttachResult`
- Reference identities: `image_item:<item-id>` and `image_attempt:<attempt-id>`

- [x] **Step 1: Write failing input reference lifecycle tests**

```go
func TestServiceSynchronizesItemInputReferences(t *testing.T) {
    service, assets := newServiceFixture(t)
    first := importImage(t, assets, "first.png")
    second := importImage(t, assets, "second.png")
    batch, _ := service.CreateBatch(validBatchInput())
    items, _ := service.CreateItems(batch.ID, []CreateItemInput{{Prompt:"p", InputAssets:InputAssets{InitImageID:first.ID}}})
    assertReference(t, assets, first.ID, "image_item", items[0].ID, true)
    _, _ = service.UpdateItem(batch.ID, items[0].ID, UpdateItemInput{Prompt:"p", ParamsOverride:json.RawMessage(`{}`), InputAssets:InputAssets{InitImageID:second.ID}})
    assertReference(t, assets, first.ID, "image_item", items[0].ID, false)
    assertReference(t, assets, second.ID, "image_item", items[0].ID, true)
}
```

Add named tests `TestServiceRejectsUnknownOrNonImageInputWithoutMutation`, `TestServiceRetainsArchivedInputReference`, `TestServiceDeleteItemAndBatchReleaseAllReferences`, and `TestServiceRollsBackWhenReferenceSynchronizationFails`. Each snapshots the Batch and Asset documents before the operation and compares both documents after the expected error.

- [x] **Step 2: Run Service lifecycle tests and verify RED**

Run: `go test ./internal/imagegen -run 'TestService(Synchronizes|Rejects|Rolls)' -count=1 -v`

- [x] **Step 3: Implement input reference diff and compensation**

Canonicalize all five InputAssets fields into a unique ordered ID list. Validate every Asset exists and has `image/` media type before Repository mutation. Apply add/remove reference diffs after mutation; on failure restore the exact old Batch through an unexported Repository rollback primitive, then compensate any already-applied reference changes.

When `CreateBatchInput.BaseParams` is empty bytes, `Service.CreateBatch` assigns `sdcpp.DefaultImageParams()` before Repository validation. A literal `{}` remains a deliberate empty Object.

- [x] **Step 4: Write failing result attachment tests**

```go
func TestServiceAttachesAttemptResultReference(t *testing.T) {
    service, assets, batchID, itemID, attemptID := resultFixture(t)
    result := importImage(t, assets, "result.png")
    got, err := service.AttachResult(batchID, itemID, attemptID, result.ID)
    if err != nil || len(got.ResultAssetIDs) != 1 || got.ResultAssetIDs[0] != result.ID { t.Fatal(got, err) }
    assertReference(t, assets, result.ID, "image_attempt", attemptID, true)
}
```

- [x] **Step 5: Run result attachment tests and verify RED**

Run: `go test ./internal/imagegen -run TestServiceAttachesAttemptResultReference -count=1 -v`

- [x] **Step 6: Implement `AttachResult` and deletion cleanup**

`AttachResult` accepts only existing `image/*` Assets, adds the attempt reference, then persists the unique ResultAssetID; it removes the new reference if persistence fails. Deleting an Item or Batch removes both input and every Attempt result reference before repository deletion with the same rollback discipline.

- [x] **Step 7: Verify and commit Task 3**

Run: `gofmt -w internal/imagegen && go test ./internal/imagegen -count=1 && git diff --check`

```bash
git add internal/imagegen
git commit -m "feat: synchronize image batch assets"
```

### Task 4: 完整参数递归合并和 Asset 请求组装

**Files:**
- Create: `internal/sdcpp/params.go`
- Create: `internal/sdcpp/params_test.go`
- Create: `internal/imagegen/assembler.go`
- Create: `internal/imagegen/assembler_test.go`

**Interfaces:**
- Produces: `sdcpp.MergeImageParams(base,override json.RawMessage) (map[string]any,error)`
- Produces: `sdcpp.RenderImageRequest(params map[string]any,prompt,negative string,images ImageFields) ([]byte,error)`
- Produces: `imagegen.NewAssembler(assets)`, `Build(batch,item,provider) (PreparedRequest,Snapshot,error)`

- [x] **Step 1: Write failing recursive merge and managed-key tests**

```go
func TestMergeImageParamsRecursesObjectsAndReplacesArrays(t *testing.T) {
    got, err := MergeImageParams(json.RawMessage(`{"width":1024,"sample_params":{"sample_steps":20,"guidance":{"txt_cfg":5}},"lora":[1]}`), json.RawMessage(`{"sample_params":{"guidance":{"txt_cfg":7}},"lora":[2]}`))
    if err != nil { t.Fatal(err) }
    if got["width"].(json.Number).String() != "1024" { t.Fatal(got) }
    sample := got["sample_params"].(map[string]any)
    if sample["sample_steps"].(json.Number).String() != "20" || sample["guidance"].(map[string]any)["txt_cfg"].(json.Number).String() != "7" { t.Fatal(got) }
    if got["lora"].([]any)[0].(json.Number).String() != "2" { t.Fatal(got) }
}

func TestMergeImageParamsRejectsManagedKeys(t *testing.T) {
    _, err := MergeImageParams(json.RawMessage(`{"prompt":"bypass"}`), json.RawMessage(`{}`))
    if !errors.Is(err, ErrManagedImageField) { t.Fatal(err) }
}
```

- [x] **Step 2: Run params tests and verify RED**

Run: `go test ./internal/sdcpp -run 'TestMergeImage' -count=1 -v`

- [x] **Step 3: Implement strict Object decoding and recursive merge**

Use `json.Decoder.UseNumber`, require exactly one non-null Object, reject the seven managed keys at the top level, recursively clone Objects, replace arrays/scalars/null, and marshal with standard `encoding/json`. Preserve unknown fields without normalization.

- [x] **Step 4: Write failing Assembler image and snapshot tests**

```go
func TestAssemblerInjectsControlledImagesWithoutPersistingDataURLs(t *testing.T) {
    assembler, assets, batch, item, provider := assemblerFixture(t)
    init := importPNG(t, assets)
    item.InputAssets.InitImageID = init.ID
    prepared, snapshot, err := assembler.Build(batch, item, provider)
    if err != nil { t.Fatal(err) }
    if !bytes.Contains(prepared.Body, []byte(`"init_image":"data:image/png;base64,`)) { t.Fatal(string(prepared.Body)) }
    encoded, _ := json.Marshal(snapshot)
    if bytes.Contains(encoded, []byte("base64")) || snapshot.InputAssets[0].SHA256 != init.SHA256 { t.Fatal(string(encoded)) }
}
```

Add named tests `TestAssemblerPreservesRefImageOrder`, `TestAssemblerAcceptsArchivedReferencedImage`, `TestAssemblerRejectsMissingOrNonImageAsset`, `TestAssemblerEnforcesTotalInputLimit`, and `TestAssemblerRedactsSensitiveHeaders`. The redaction test covers `Authorization`, `Proxy-Authorization`, `X-API-Key` and `API-Key` case-insensitively.

- [x] **Step 5: Run Assembler tests and verify RED**

Run: `go test ./internal/imagegen -run TestAssembler -count=1 -v`

- [x] **Step 6: Implement `Assembler.Build`**

`PreparedRequest` contains URL, headers, body and timeouts. Snapshot copies canonical merged params before managed image injection, prompt fields, Provider with redacted headers, and AssetSnapshot metadata. Read each Asset with `io.LimitReader`; Base64 exists only in the returned Body.

- [x] **Step 7: Verify and commit Task 4**

Run: `gofmt -w internal/sdcpp internal/imagegen && go test ./internal/sdcpp ./internal/imagegen -count=1 && git diff --check`

```bash
git add internal/sdcpp internal/imagegen
git commit -m "feat: assemble native image requests"
```

### Task 5: stable-diffusion.cpp capabilities 和异步 Job Client

**Files:**
- Create: `internal/sdcpp/client.go`
- Create: `internal/sdcpp/client_test.go`

**Interfaces:**
- Produces DTOs: `Capabilities`, `Submission`, `Job`, `JobResult`, `JobImage`, `RemoteError`
- Produces: `Client.Capabilities`, `Submit`, `Job`, `Cancel`
- All methods consume `context.Context` and `ImageProvider`; Submit consumes rendered `[]byte`

Use these signatures:

```go
func (Client) Capabilities(context.Context, ImageProvider) (Capabilities,error)
func (Client) Submit(context.Context, ImageProvider, []byte) (Submission,error)
func (Client) Job(context.Context, ImageProvider, string) (Job,error)
func (Client) Cancel(context.Context, ImageProvider, string) error
```

`Capabilities` keeps Model, SupportedModes, DefaultsByMode, OutputFormatsByMode, FeaturesByMode, Samplers, Schedulers, Loras, Upscalers and Limits as typed fields plus `json.RawMessage` for unknown mode metadata. `JobResult` has OutputFormat and `[]JobImage`; `JobImage` has Index and B64JSON. `Job` has ID, Kind, Status, QueuePosition, Result and RemoteError.

- [x] **Step 1: Write failing capabilities and submission tests**

```go
func TestClientReadsCapabilitiesAndSubmitsNativeImageJob(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/sdcpp/v1/capabilities":
            io.WriteString(w, `{"supported_modes":["img_gen"],"samplers":["euler"],"defaults_by_mode":{"img_gen":{"width":768}}}`)
        case "/sdcpp/v1/img_gen":
            w.WriteHeader(http.StatusAccepted)
            io.WriteString(w, `{"id":"job-a","kind":"img_gen","status":"queued","poll_url":"/sdcpp/v1/jobs/job-a"}`)
        default: t.Fatalf("path=%s", r.URL.Path)
        }
    }))
    defer server.Close()
    provider := testProvider(server.URL)
    capabilities, err := (Client{}).Capabilities(context.Background(), provider)
    if err != nil || capabilities.Samplers[0] != "euler" { t.Fatal(capabilities, err) }
    submitted, err := (Client{}).Submit(context.Background(), provider, []byte(`{"prompt":"cat"}`))
    if err != nil || submitted.ID != "job-a" { t.Fatal(submitted, err) }
}
```

- [x] **Step 2: Run Client tests and verify RED**

Run: `go test ./internal/sdcpp -run TestClient -count=1 -v`

- [x] **Step 3: Implement bounded HTTP transport and strict DTO validation**

Build a per-call `http.Client` with `net.Dialer.Timeout`, no proxy override, and redirect rejection. Join fixed paths to canonical BaseURL; URL-escape RemoteJobID. Require 2xx for capabilities/job/cancel and exactly 202 for submit. Read at most `MaxResponseBytes+1`, require one JSON document, verify Job kind `img_gen`, valid state and non-empty ID. Do not follow or request `poll_url`.

- [x] **Step 4: Write failing completion/error/cancel/limit tests**

```go
func TestClientReadsCompletedJobAndCancelsByID(t *testing.T) {
    requested := make(chan string, 2)
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        requested <- r.Method + " " + r.URL.EscapedPath()
        if r.Method == http.MethodGet {
            io.WriteString(w, `{"id":"job-a","kind":"img_gen","status":"completed","queue_position":0,"result":{"output_format":"png","images":[{"index":0,"b64_json":"aW1hZ2U="}]},"error":null}`)
            return
        }
        io.WriteString(w, `{"id":"job-a","kind":"img_gen","status":"cancelled","queue_position":0,"result":null,"error":{"code":"cancelled","message":"cancelled"}}`)
    }))
    defer server.Close()
    provider := testProvider(server.URL)
    job, err := (Client{}).Job(context.Background(), provider, "job-a")
    if err != nil || job.Result.Images[0].Index != 0 { t.Fatal(job, err) }
    if err := (Client{}).Cancel(context.Background(), provider, "job-a"); err != nil { t.Fatal(err) }
    if got := <-requested; got != "GET /sdcpp/v1/jobs/job-a" { t.Fatal(got) }
    if got := <-requested; got != "POST /sdcpp/v1/jobs/job-a/cancel" { t.Fatal(got) }
}
```

Add table-driven `TestClientReturnsTypedHTTPError` for 404/409/410, plus `TestClientRejectsOversizedResponse`, `TestClientRejectsInvalidJSON`, `TestClientHonorsContextCancellation`, and `TestClientBoundsErrorBodyTo4096Bytes` using real `httptest.Server` handlers.

- [x] **Step 5: Run completion/error/cancel tests and verify RED**

Run: `go test ./internal/sdcpp -run 'TestClient(ReadsCompleted|ReturnsTyped|Rejects|Honors|Bounds)' -count=1 -v`

- [x] **Step 6: Implement Job and Cancel methods plus error types**

`HTTPError` exposes StatusCode and at most 4096 response bytes. `JobResult.Images` retains Base64 strings only in memory DTOs. Cancel accepts 200 only; Manager handles 404/409/410 recovery policy.

- [x] **Step 7: Verify and commit Task 5**

Run: `gofmt -w internal/sdcpp && go test ./internal/sdcpp -count=1 && git diff --check`

```bash
git add internal/sdcpp
git commit -m "feat: call native image job API"
```

### Task 6: 有限并发 Manager、轮询、导入、取消和事件

**Files:**
- Create: `internal/imagegen/manager.go`
- Create: `internal/imagegen/manager_test.go`

**Interfaces:**
- Consumes: `*config.Repository`, `*imagegen.Service`, `*imagegen.Assembler`, `*asset.Repository`, `RemoteClient`
- Produces: `NewManager(...)`, `StartBatch`, `StartItem`, `Cancel`, `GetAttempt`, `SubscribeBatch`, `Shutdown`
- Produces: `AttemptEvent{Type string, Attempt Attempt}` with event types `snapshot|state`

```go
type RemoteClient interface {
    Submit(context.Context,sdcpp.ImageProvider,[]byte) (sdcpp.Submission,error)
    Job(context.Context,sdcpp.ImageProvider,string) (sdcpp.Job,error)
    Cancel(context.Context,sdcpp.ImageProvider,string) error
}
func (m *Manager) StartBatch(batchID string) ([]Attempt,error)
func (m *Manager) StartItem(batchID,itemID string) (Attempt,error)
func (m *Manager) Cancel(attemptID string) error
func (m *Manager) GetAttempt(attemptID string) (Attempt,bool)
func (m *Manager) SubscribeBatch(batchID string) (<-chan AttemptEvent,func(),error)
func (m *Manager) Shutdown(context.Context) error
```

- [x] **Step 1: Write failing ordered batch and Provider concurrency tests**

```go
func TestManagerRunsBatchInOrderWithinProviderConcurrency(t *testing.T) {
    fixture := newManagerFixture(t, 1)
    batch := fixture.batchWithPrompts("one", "two", "three")
    attempts, err := fixture.manager.StartBatch(batch.ID)
    if err != nil || len(attempts) != 3 { t.Fatal(attempts, err) }
    fixture.remote.releaseAll()
    fixture.waitTerminal(attempts)
    if got := fixture.remote.submittedPrompts(); !reflect.DeepEqual(got, []string{"one","two","three"}) { t.Fatal(got) }
    if fixture.remote.maximumActive() != 1 { t.Fatal(fixture.remote.maximumActive()) }
}
```

Add named tests `TestManagerSharesProviderSemaphoreAcrossBatches`, `TestManagerHonorsLowerBatchConcurrency`, and `TestManagerRejectsDisabledMissingProviderAndActiveItem`. The fake RemoteClient records active calls under a mutex and exposes a release channel so each assertion is deterministic.

- [x] **Step 2: Run Manager scheduling tests and verify RED**

Run: `go test ./internal/imagegen -run 'TestManager(Runs|Shares|Rejects)' -count=1 -v`

- [x] **Step 3: Implement scheduling and immutable preflight snapshots**

`StartBatch` visits Item Order and creates one queued Attempt for each Item without active Attempt. `StartItem` creates a fresh Attempt even after terminal history, but rejects an active latest Attempt. Build and persist Snapshot before enqueuing; if Build fails, persist a failed Attempt with bounded error. Manager owns one semaphore per Provider ID and a Batch semaphore per active batch configuration.

- [x] **Step 4: Write failing poll/import/partial result tests**

```go
func TestManagerImportsEveryCompletedImageAsArchiveAsset(t *testing.T) {
    fixture := newManagerFixture(t, 1)
    fixture.remote.completeWith([]remoteImage{validPNG(0), validPNG(1)})
    attempt := fixture.startOneAndWait()
    if attempt.State != AttemptSucceeded || len(attempt.ResultAssetIDs) != 2 { t.Fatal(attempt) }
    for _, id := range attempt.ResultAssetIDs {
        item, _ := fixture.assets.Get(id)
        if item.State != asset.StateArchive || item.Source != "imagegen:"+attempt.ID { t.Fatal(item) }
    }
}
```

Add PNG/JPEG/WebP sniff tests, invalid Base64, single-image limit, format mismatch and partial second-image failure retaining the first Asset while Attempt becomes failed.

- [x] **Step 5: Run poll/import tests and verify RED**

Run: `go test ./internal/imagegen -run 'TestManagerImports|TestManagerRejectsInvalidResult|TestManagerRetainsPartial' -count=1 -v`

- [x] **Step 6: Implement submit/poll/result import state machine**

Use JobTimeout Context from Provider snapshot. Persist submitting, Submit, RemoteJobID/polling, each changed remote status/queue position, then terminal state. Poll with resettable `time.Timer`, not `time.Sleep`. Decode each Base64 with `base64.StdEncoding.Strict`, enforce decoded size before `asset.Import`, sniff PNG/JPEG/WebP signatures and call `Service.AttachResult` immediately after each import.

- [x] **Step 7: Write failing cancellation, subscription and shutdown tests**

```go
func TestManagerCancelCallsRemoteAndPublishesTerminalState(t *testing.T) {
    fixture := newBlockingManagerFixture(t)
    attempt := fixture.startOneAndWaitForRemoteID()
    events, unsubscribe, _ := fixture.manager.SubscribeBatch(fixture.batchID)
    defer unsubscribe()
    if err := fixture.manager.Cancel(attempt.ID); err != nil { t.Fatal(err) }
    got := fixture.waitAttemptState(events, AttemptCancelled)
    if got.ID != attempt.ID || !fixture.remote.cancelled(attempt.RemoteJobID) { t.Fatal(got) }
}
```

Add queued cancellation without remote call, 409 followed by completed Job import, initial subscription snapshots, slow subscriber bounded behavior, Shutdown refusing new jobs and waiting, and Context cancellation marking active Attempts cancelled rather than succeeded.

- [x] **Step 8: Run cancellation/subscription/shutdown tests and verify RED**

Run: `go test ./internal/imagegen -run 'TestManager(Cancel|Subscribe|Shutdown)' -count=1 -v`

- [x] **Step 9: Implement cancellation/events/shutdown**

Index active Attempt cancel funcs by ID and batch subscribers by Batch ID. `SubscribeBatch` first emits latest Attempts in Item order. On terminal close only that Attempt's worker; batch stream stays available until subscriber disconnect. `Shutdown` flips accepting false, cancels workers, best-effort cancels remote Jobs within the caller deadline, and waits on one WaitGroup.

- [x] **Step 10: Verify race safety and commit Task 6**

Run: `gofmt -w internal/imagegen && go test -race ./internal/imagegen ./internal/sdcpp -count=1 && git diff --check`

```bash
git add internal/imagegen
git commit -m "feat: manage image generation jobs"
```

Task 6 completion note (2026-08-31): the Manager now serializes worker/cancel recovery transitions, keeps concurrency limits resizable without replacing active permits, retries terminal persistence with backoff, preserves partial Asset results, and waits for accepted starts during shutdown. Full repository tests, race tests, vet, diff checks, and focused code review passed before commit.

### Task 7: Image Config、Batch、Attempt API 和 App 生命周期

**Files:**
- Create: `internal/web/image_config.go`
- Create: `internal/web/image_config_test.go`
- Create: `internal/web/image_batch.go`
- Create: `internal/web/image_batch_test.go`
- Create: `internal/web/image_attempt.go`
- Create: `internal/web/image_attempt_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Produces routes from design §11
- App owns `imagegen.Manager` and includes it in graceful shutdown
- Web `Options` gains Config repository, Image Service/Manager and sdcpp Client capability dependencies

- [x] **Step 1: Write failing image config and capabilities API tests**

```go
func TestImageConfigAPIUpdatesCompleteConfiguration(t *testing.T) {
    handler, repository := imageConfigFixture(t)
    body := `{"providers":[{"id":"gpu","name":"GPU","base_url":"http://127.0.0.1:1234","headers":{},"connect_timeout_seconds":10,"job_timeout_seconds":3600,"poll_interval_milliseconds":750,"max_response_bytes":268435456,"max_image_bytes":134217728,"max_concurrent_jobs":1,"enabled":true}]}`
    response := performJSON(handler, http.MethodPut, "/api/v1/images/config", body)
    if response.Code != http.StatusOK || repository.Snapshot().Images.Providers[0].ID != "gpu" { t.Fatal(response.Body.String()) }
}
```

Add GET, strict unknown field, invalid provider, missing capability Provider, disabled Provider and proxied capabilities response tests.

- [x] **Step 2: Run Config/capabilities API tests and verify RED**

Run: `go test ./internal/web -run 'TestImage(Config|Capabilities)' -count=1 -v`

- [x] **Step 3: Implement Config and capabilities handlers**

Use existing `decodeStrictJSON` and Error Envelope. Capabilities handler reads the latest config, finds an enabled Provider and calls injected `sdcpp.Client`; map timeout to 504, non-2xx to 502 and config errors to 400/404.

- [x] **Step 4: Write failing Batch/Item CRUD API tests**

Cover collection GET/POST, folder/query filtering, Batch GET/PUT/DELETE, multi-Item POST, Item PUT/DELETE and `{"direction":-1|1}` move. Assert unknown JSON fields fail and response JSON returns canonical Order plus archive input Asset summaries.

- [x] **Step 5: Run Batch/Item CRUD API tests and verify RED**

Run: `go test ./internal/web -run 'TestImage(Batch|Item)' -count=1 -v`

- [x] **Step 6: Implement Batch and Item handlers**

Validate all path IDs as 32-hex. Return 201 for create, 200 for updates, 204 for deletes. Map active Attempt conflicts to 409, missing resources to 404, invalid params/reference to 400 and storage errors to 500 without leaking paths.

- [x] **Step 7: Write failing execute/cancel/SSE tests**

```go
func TestImageAttemptSSEWritesSnapshotAndState(t *testing.T) {
    manager := imageManagerFixture(t)
    handler := newImageHandler(manager)
    attempt := manager.startFixtureAttempt(t)
    requestContext, cancel := context.WithCancel(context.Background())
    request := httptest.NewRequest(http.MethodGet, "/api/v1/images/batches/"+manager.batchID+"/events", nil).WithContext(requestContext)
    response := newStreamingRecorder()
    done := make(chan struct{})
    go func() { handler.ServeHTTP(response, request); close(done) }()
    response.waitFor(t, "event: snapshot")
    manager.publishFixtureState(attempt.ID, imagegen.AttemptPolling)
    response.waitFor(t, "event: state")
    cancel()
    <-done
    if !strings.Contains(response.String(), attempt.ID) { t.Fatal(response.String()) }
}
```

Add Batch execute 202, Item retry 202, duplicate active 409, Attempt GET, cancel accepted/terminal idempotence, SSE heartbeat and disconnected request cleanup.

- [x] **Step 8: Run execute/cancel/SSE tests and verify RED**

Run: `go test ./internal/web -run 'TestImage(Execute|Attempt|Cancel)' -count=1 -v`

- [x] **Step 9: Implement execute, Attempt and SSE handlers**

Batch execute response is `{"attempts":[...]}`. SSE route sets no-cache/X-Accel headers, writes one event per `AttemptEvent`, heartbeat every 15 seconds and exits on request Context. JSON never includes output Base64 because Attempt stores only Asset IDs.

- [x] **Step 10: Write failing app assembly and shutdown tests**

Extend the real app lifecycle fixture to assert `<data-dir>/images/batches` creation, default image config API, Batch creation persistence, activity cancelled during shutdown, and queued/polling fixture reopened as interrupted.

- [x] **Step 11: Run app image lifecycle tests and verify RED**

Run: `go test ./internal/app -run 'TestApplication(Image|ShutdownCancelsImage)' -count=1 -v`

- [x] **Step 12: Wire application runtime and shutdown order**

Open `imagegen.Repository` under `images/batches`, wrap Service, construct Assembler and Manager, pass dependencies into Web. Add `imageManager` to `applicationRuntime`; shutdown joins image Manager, LLM Manager and backend Manager within the existing timeout.

- [x] **Step 13: Verify and commit Task 7**

Run: `gofmt -w internal/web internal/app && go test -race ./internal/web ./internal/app ./internal/imagegen -count=1 && git diff --check`

```bash
git add internal/web internal/app
git commit -m "feat: expose image generation API"
```

Task 7 completion note (2026-08-31): the native API now covers image Provider configuration/capabilities, Batch and Item CRUD, execution, Attempt lookup/cancellation, and Batch SSE. Batch detail includes Provider availability and referenced Asset summaries; application shutdown closes SSE and stops HTTP plus all managers concurrently within one deadline. Strict Object decoding, upstream status mapping, storage-error redaction, active-delete protection, and app persistence/shutdown behavior are covered by tests and focused review.

### Task 8: 原生 Image Provider 配置界面

**Files:**
- Create: `internal/web/static/image-config.js`
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/server.go`
- Modify: `internal/web/server_test.go`

**Interfaces:**
- Produces: `createImageConfig({readAPIError})` with `enter()`/`leave()`
- Consumes: `GET/PUT /api/v1/images/config` and capabilities endpoint
- `app.js` only wires lifecycle; configuration domain logic stays in `image-config.js`

- [ ] **Step 1: Write failing embedded Image Config contract test**

Assert Settings markup has image Provider list/new/editor, Base URL, headers, timeouts, polling, byte limits, concurrency, enabled, capability test and save status. Assert `/assets/image-config.js` is served, exports `createImageConfig` and contains both image config/capabilities API paths.

- [ ] **Step 2: Run contract test and verify RED**

Run: `go test ./internal/web -run TestEmbeddedImageConfig -count=1 -v`

- [ ] **Step 3: Implement progressive Provider editor**

Default row shows name, URL, enabled and edit/test actions. Dialog basic section shows ID/name/Base URL/enabled/concurrency; `<details>` contains Headers JSON, timeouts, polling and byte limits. Validate Headers as a string-valued Object and numeric ranges locally, but render the authoritative server error beside the form. Save sends one complete `ImageConfig` object.

- [ ] **Step 4: Wire Settings lifecycle and responsive styles**

Import `createImageConfig` in `app.js`, instantiate once, call enter only for Settings and leave otherwise alongside existing LLM config controller. Add compact config rows and near-full-screen mobile Dialog rules without changing other modules.

- [ ] **Step 5: Verify and commit Task 8**

Run: `go test ./internal/web ./internal/app -count=1 && git diff --check`

```bash
git add internal/web
git commit -m "feat: configure image providers in browser"
```

### Task 9: 响应式生图 Batch、Item 和结果工作区

**Files:**
- Create: `internal/web/static/images.js`
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/server.go`
- Modify: `internal/web/server_test.go`

**Interfaces:**
- Produces: `createImageWorkspace({sidebarContent,sidebarSearch,readAPIError,openAssetPicker})`
- Consumes: image Batch/Item/Attempt/SSE APIs, capabilities, Asset API and active-only picker
- `app.js` only calls `enter()`/`leave()` from module selection

- [ ] **Step 1: Write failing Image workspace contract test**

Assert served markup exposes Batch search/folder/new/list; title/folder/Provider/concurrency/save/delete/capabilities; Base Params common fields and advanced JSON; bulk prompts; Item prompt/negative/override/assets/up/down/copy/delete/run/retry/cancel; result grid, active/archive actions, Attempt history and technical details. Assert `images.js` export, Batch APIs, `new EventSource`, `openAssetPicker` and Asset state API.

- [ ] **Step 2: Run contract test and verify RED**

Run: `go test ./internal/web -run TestEmbeddedImageWorkspace -count=1 -v`

- [ ] **Step 3: Implement module lifecycle and Batch sidebar**

Capture image sidebar controls before Shell replacement. `enter` loads config and Batches, restores selected Batch, enables shared search and attaches Batch SSE; `leave` closes EventSource and disables shared search. Filter locally by title/folder and render only server-returned canonical objects.

- [ ] **Step 4: Implement Batch and parameter editing**

Save title/folder/Provider/concurrency/BaseParams explicitly. Common controls read/write the same parsed BaseParams Object at paths `width`, `height`, `seed`, `batch_count`, `sample_params.sample_steps`, `sample_params.guidance.txt_cfg`, `sample_params.sample_method`, `sample_params.scheduler`, `output_format`. Advanced textarea is the complete Object; switching sections reserializes deterministically with two-space indentation. Capabilities may fill datalists/limits or explicitly replace draft defaults only after confirmation.

- [ ] **Step 5: Implement Item editor and active Asset references**

Bulk dialog converts trimmed non-empty lines into an explicit Item array. Item Dialog edits prompt, negative, full override Object and five input groups. Call `openAssetPicker` for each; retain existing non-active IDs by fetching their metadata and show each reference with an explicit remove button. Up/down are text buttons calling move API; copy creates a new Item with identical fields after the source.

- [ ] **Step 6: Implement execution, Attempt SSE and result cards**

Run pending Batch or one Item with explicit buttons. Render latest state on each Item and history in collapsed details. Batch EventSource updates by Attempt ID using `textContent`, then refreshes authoritative Batch on terminal state. Cancel only active Attempt; retry creates a new Attempt. Result images use `/api/v1/assets/{id}/content`; active/archive buttons call existing Asset state API and refresh the card.

- [ ] **Step 7: Implement responsive progressive-disclosure styling**

Desktop keeps existing left sidebar and uses a wide single Batch column plus responsive result grid. ≤760px makes toolbar/common parameters/Item rows single-column, wraps actions, and uses near-full-screen dialogs. Advanced JSON, history, snapshots and errors remain in `<details>`; there is no drag-only operation.

- [ ] **Step 8: Verify and commit Task 9**

Run: `go test ./internal/web ./internal/app -count=1 && git diff --check`

```bash
git add internal/web
git commit -m "feat: add image batch workspace"
```

### Task 10: 文档、完整验证、合并和推送

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-08-30-image-workspace.md`
- Modify: `docs/superpowers/specs/2026-08-30-image-workspace-design.md`
- Modify: `docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md`

**Interfaces:**
- Produces: 用户运行说明、Provider/参数说明、数据路径、Asset 流程、恢复语义和阶段完成状态

- [ ] **Step 1: Update README with exact image workflows**

Document `<data-dir>/images/batches`, default `sdcpp-local`, backend-management separation, capabilities, common/advanced params merge, reserved managed fields, active input/archive output behavior, batch concurrency, retry/cancel and interrupted Attempt semantics.

- [ ] **Step 2: Run full static and test verification**

Run: `gofmt -w cmd internal && go vet ./... && go test ./... -count=1`

Run: `go test -race ./internal/config ./internal/sdcpp ./internal/imagegen ./internal/asset ./internal/web ./internal/app -count=1`

Run: `VERIFY_DIR=$(mktemp -d) && go build -o "$VERIFY_DIR/ai-workbench" ./cmd/ai-workbench && git diff --check`

- [ ] **Step 3: Run real binary HTTP smoke test**

Start the built binary on an unused loopback port with a temporary data dir. Use `curl` to read the default image Provider, create a Batch and Items, load `/assets/images.js`, verify the Batch file exists, send SIGINT and verify the port closes cleanly.

- [ ] **Step 4: Review requirements and close documentation**

Check the design line by line, scan for placeholders/contradictions, mark design and master phase 5 completed, then commit:

```bash
git add README.md docs
git commit -m "docs: record image workspace delivery"
```

- [ ] **Step 5: Merge, push and verify remote**

Fast-forward the approved feature branch into `main`, rerun `go test ./... -count=1`, mark this final checkbox in a main documentation commit, `git push origin main`, assert `HEAD == origin/main` and clean status, then safely remove the local feature worktree and branch. Preserve the remote feature branch unless the user explicitly requests deletion.
