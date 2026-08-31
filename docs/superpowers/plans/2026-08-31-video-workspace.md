# 视频工作区实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付独立的视频批次工作区，通过 stable-diffusion.cpp 原生异步 HTTP 或受信任的本机 CLI 批量生成视频，并把视频与外部命令提取的尾帧纳入共享 Asset 生命周期。

**Architecture:** `internal/videoconfig` 定义 HTTP、CLI 和尾帧预设并接入主配置 Schema 4；`internal/videogen` 拥有 Batch/Item/Attempt、Asset 引用、HTTP 组装、本机固定工作区、调度和尾帧提取。`internal/sdcpp` 增加严格的 `vid_gen` DTO/Client，`internal/web` 提供 JSON/SSE API 与两个原生 JavaScript 控制器；视频不会复用生图领域对象，也不会通过 Remote Worker 传输素材或结果。

**Tech Stack:** Linux、Go 1.24 标准库、`net/http`、版本化原子 JSON、Linux process groups、SSE、原生 HTML/CSS/JavaScript

**Spec:** `docs/superpowers/specs/2026-08-31-video-workspace-design.md`

## Global Constraints

- Go 和浏览器端不增加第三方依赖、CDN、包管理器或构建步骤。
- 视频 Provider、Batch、Item、Attempt、参数对象和页面均与生图隔离；只复用 Asset、Shell、SSE 和受限 HTTP 原语。
- HTTP 只调用 stable-diffusion.cpp 官方 `/sdcpp/v1/vid_gen`、Job 查询和取消接口；浏览器不直连模型 Server。
- HTTP 输入只允许 Init Image、End Image 和有序 Control Frames；`ref_video`、参考音频等高级输入只走本机 CLI。
- CLI 固定为 `local_cli`，不通过 Remote Worker；每个 Attempt 只暂存用户明确选择的 Asset，只读取声明的唯一输出路径。
- Attempt 必须在任何外部 HTTP、准备命令或主命令前持久化不可变快照；快照不保存 Data URL 或文件内容。
- 输入选择器只展示 active Asset；成功引用后即使归档仍保留，直到用户明确移除。
- 视频与尾帧结果默认导入 archive Asset；部分已导入结果不回滚，不自动精选。
- 所有 HTTP 正文、错误、输入媒体、输出视频、尾帧图片、CLI 日志和工作区路径都有硬上限。
- 页面默认保持简洁；完整 JSON、换算、快照、Job ID、CLI 路径和日志放入 `<details>`，必要错误直接显示。
- 每项实现测试先行：先观察目标失败，再写最小实现；每个 Task 独立提交。

## 文件结构

- Create `internal/videoconfig/config.go`：三类预设、默认值、克隆与严格校验。
- Modify `internal/config/*`：Schema 3→4 迁移和运行时配置更新。
- Create `internal/videogen/model.go`、`repository.go`：独立 Batch/Item/Attempt 与原子持久化。
- Create `internal/videogen/service.go`：Asset 输入/结果引用一致性。
- Create `internal/videogen/params.go`、`http_assembler.go`：递归参数、时长换算和 HTTP 请求快照。
- Create `internal/sdcpp/video_client.go`：`vid_gen` 提交、查询、取消与视频结果 DTO。
- Create `internal/videogen/workspace.go`、`template.go`：CLI 工作区、Manifest、硬链接/复制和安全模板变量。
- Create `internal/videogen/process_linux.go`：准备命令/主命令进程组、有限日志和精确输出校验。
- Create `internal/videogen/manager.go`：HTTP/CLI 调度、并发、取消、导入、事件与关闭。
- Create `internal/videogen/tail.go`：尾帧提取记录、进程和导入。
- Create `internal/web/video_*.go`：配置、Batch、Attempt、CLI 日志/清理和尾帧 API。
- Create `internal/web/static/video-config.js`、`videos.js`：设置与视频工作区控制器。
- Modify `internal/app/app.go`、现有 Shell 与文档：运行时组装、关闭、响应式页面和阶段状态。

---

### Task 1: 三类视频预设、Schema 4 迁移和配置 Repository

**Files:**
- Create: `internal/videoconfig/config.go`
- Create: `internal/videoconfig/config_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/repository.go`
- Modify: `internal/config/repository_test.go`

**Interfaces:**
- Produces: `videoconfig.Config`, `HTTPProvider`, `CLIPreset`, `TailFramePreset`, `Default()`, `DefaultHTTPParams()`, `Validate()`, `Clone()`
- Changes: `config.CurrentSchemaVersion` from 3 to 4 and adds `Config.Videos videoconfig.Config`
- Produces: `(*config.Repository).UpdateVideos(videoconfig.Config) (config.Config,error)`

- [ ] **Step 1: 写失败的默认配置和边界测试**

```go
func TestDefaultConfigContainsHTTPVideoProvider(t *testing.T) {
    cfg := Default()
    if err := cfg.Validate(); err != nil { t.Fatal(err) }
    got := cfg.HTTPProviders[0]
    if got.ID != "sdcpp-video-local" || got.BaseURL != "http://127.0.0.1:1234" || got.MaxConcurrentJobs != 1 { t.Fatal(got) }
}

func TestConfigRejectsEscapingCLIOutputAndUnsafeHeaders(t *testing.T) {
    cfg := Default()
    cfg.CLIPresets = []CLIPreset{validCLIPreset()}
    cfg.CLIPresets[0].OutputRelativePath = "../result.webm"
    if err := cfg.Validate(); err == nil { t.Fatal("escaping output accepted") }
    cfg = Default()
    cfg.HTTPProviders[0].Headers["X-Test"] = "ok\nInjected: yes"
    if err := cfg.Validate(); err == nil { t.Fatal("header injection accepted") }
}
```

- [ ] **Step 2: 运行配置测试并确认 RED**

Run: `go test ./internal/videoconfig -run 'Test(DefaultConfig|ConfigRejects)' -count=1 -v`

Expected: FAIL，因为 `internal/videoconfig` 尚不存在。

- [ ] **Step 3: 实现精确配置模型与默认值**

```go
type Config struct {
    HTTPProviders   []HTTPProvider   `json:"http_providers"`
    CLIPresets      []CLIPreset      `json:"cli_presets"`
    TailFramePresets []TailFramePreset `json:"tail_frame_presets"`
}
type ExecutionKind string
const (
    ExecutionHTTP ExecutionKind = "http"
    ExecutionLocalCLI ExecutionKind = "local_cli"
)
type HTTPProvider struct {
    ID, Name, BaseURL string; Headers map[string]string
    ConnectTimeoutSeconds, JobTimeoutSeconds, PollIntervalMilliseconds int
    MaxRequestBytes, MaxErrorBytes, MaxVideoBytes, MaxInputImageBytes int64
    MaxConcurrentJobs int; Enabled bool; DefaultParams json.RawMessage
}
type CLIPreset struct {
    ID, Name string; Enabled bool; ExecutionKind ExecutionKind
    PrepareCommandTemplate, CommandTemplate, WorkDir string; Env map[string]string
    TimeoutSeconds, StopGraceSeconds, LogBufferBytes int
    OutputRelativePath, OutputMediaType, OutputExtension string; MaxOutputBytes int64
    DefaultParams json.RawMessage
}
type TailFramePreset struct {
    ID, Name string; Enabled bool; CommandTemplate string
    TimeoutSeconds, StopGraceSeconds int; MaxImageBytes int64; OutputExtension string
}
```

所有 JSON Tag 使用对应 snake_case。ID 为 1–120 个安全字符且跨同类唯一；Base URL 为无查询/Fragment/尾斜杠的 HTTP(S)；Header 值拒绝换行。HTTP 超时为 1–86400 秒、轮询 100–10000 ms、并发 1–16；Request 最大 2 GiB、Error 最大 64 KiB、视频最大 4 GiB、输入图片最大 1 GiB。默认依次为 10 秒、86400 秒、750 ms、1、384 MiB、64 KiB、1 GiB、256 MiB，确保默认输入图片转成 Data URL 后仍落在 Request 上限内。CLI 只接受 `execution_kind:"local_cli"`，非空 WorkDir 必须绝对，Env Key 合法，命令/模板有长度上限，输出相对路径必须清理后仍严格位于 `outputs/`，MaxOutputBytes 为 1 Byte–4 GiB。Tail 输出扩展名只允许 `.png`、`.jpg`、`.jpeg`、`.webp`。

`DefaultHTTPParams()` 返回新副本：

```json
{"width":832,"height":480,"seed":-1,"output_format":"webm","sample_params":{"sample_steps":28}}
```

- [ ] **Step 4: 写失败的 Schema 3→4 迁移和深拷贝测试**

```go
func TestLoadMigratesSchemaThreeWithVideoDefaults(t *testing.T) {
    path := filepath.Join(t.TempDir(), "config.json")
    old := Default(); old.SchemaVersion = 3; old.Videos = videoconfig.Config{}
    writeConfigFixture(t, path, old)
    got, err := Load(path)
    if err != nil || got.SchemaVersion != 4 || got.Videos.HTTPProviders[0].ID != "sdcpp-video-local" { t.Fatal(got, err) }
}

func TestRepositoryUpdateVideosPersistsDeepCopy(t *testing.T) {
    repository := openConfigFixture(t)
    videos := repository.Snapshot().Videos
    videos.HTTPProviders[0].Headers["X-Test"] = "before"
    if _, err := repository.UpdateVideos(videos); err != nil { t.Fatal(err) }
    videos.HTTPProviders[0].Headers["X-Test"] = "after"
    if repository.Snapshot().Videos.HTTPProviders[0].Headers["X-Test"] != "before" { t.Fatal("alias retained") }
}
```

- [ ] **Step 5: 实现迁移、验证委托和 `UpdateVideos`**

```go
const CurrentSchemaVersion = 4

type Config struct {
    // existing fields...
    Videos videoconfig.Config `json:"videos"`
}

func (repository *Repository) UpdateVideos(videos videoconfig.Config) (Config, error) {
    candidate := videos.Clone()
    if err := candidate.Validate(); err != nil { return Config{}, fmt.Errorf("validate video config: %w", err) }
    return repository.update(func(cfg *Config) { cfg.Videos = candidate })
}
```

新增 unexported `update(change func(*Config)) (Config,error)`，在 Repository Mutex 内克隆、变更、`Save` 再替换内存快照；现有 `UpdateLLM/UpdateImages` 改走该 Helper，保持语义。迁移只填入 `videoconfig.Default()`，保持所有旧字段；`Config.Clone` 深拷贝 Header、Env 和 RawMessage。

- [ ] **Step 6: 验证并提交 Task 1**

Run: `gofmt -w internal/videoconfig internal/config && go test ./internal/videoconfig ./internal/config -count=1 && git diff --check`

```bash
git add internal/videoconfig internal/config
git commit -m "feat: configure video execution presets"
```

### Task 2: 视频 Batch、Item、Attempt 模型与持久化

**Files:**
- Create: `internal/videogen/model.go`
- Create: `internal/videogen/repository.go`
- Create: `internal/videogen/repository_test.go`

**Interfaces:**
- Produces: `Batch`, `Item`, `TimingInput`, `ResolvedTiming`, `SelectedAsset`, `Attempt`, `Snapshot`, `AssetSnapshot`, `AttemptState`
- Produces: `OpenRepository(root string) (*Repository,error)` and Batch/Item/Attempt CRUD
- Consumes: `videoconfig.ExecutionHTTP|ExecutionLocalCLI` and stable preset IDs

```go
func (r *Repository) CreateBatch(CreateBatchInput) (Batch,error)
func (r *Repository) List(Filter) []Batch
func (r *Repository) Get(batchID string) (Batch,bool)
func (r *Repository) UpdateBatch(batchID string, input UpdateBatchInput) (Batch,error)
func (r *Repository) DeleteBatch(batchID string) error
func (r *Repository) CreateItems(batchID string, inputs []CreateItemInput) ([]Item,error)
func (r *Repository) UpdateItem(batchID,itemID string,input UpdateItemInput) (Item,error)
func (r *Repository) DeleteItem(batchID,itemID string) error
func (r *Repository) MoveItem(batchID,itemID string,offset int) (Batch,error)
func (r *Repository) CreateAttempt(batchID,itemID string,input CreateAttemptInput) (Attempt,error)
func (r *Repository) UpdateAttempt(batchID,itemID,attemptID string,input UpdateAttemptInput) (Attempt,error)
func (r *Repository) AttachResult(batchID,itemID,attemptID,assetID string) (Attempt,error)
```

- [ ] **Step 1: 写失败的 Batch/Item 顺序和 Timing 校验测试**

```go
func TestRepositoryPersistsOrderedVideoItems(t *testing.T) {
    root := t.TempDir(); repository, _ := OpenRepository(root)
    batch, _ := repository.CreateBatch(CreateBatchInput{Title:"Shots", Folder:"film", ExecutionKind:"http", PresetID:"gpu", Concurrency:1, CommonParams:json.RawMessage(`{}`), Timing:TimingInput{Mode:"duration", DurationSeconds:2.5, FPS:16}})
    items, _ := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt:"one",Enabled:true},{Prompt:"two",Enabled:true}})
    if _, err := repository.MoveItem(batch.ID, items[1].ID, -1); err != nil { t.Fatal(err) }
    reopened, _ := OpenRepository(root)
    got, _ := reopened.Get(batch.ID)
    if got.Items[0].Prompt != "two" || got.Items[0].Order != 0 { t.Fatal(got.Items) }
}

func TestRepositoryRejectsAmbiguousTiming(t *testing.T) {
    input := validBatchInput(); input.Timing = TimingInput{Mode:"duration",DurationSeconds:2,FPS:0,VideoFrames:33}
    if _, err := newRepositoryFixture(t).CreateBatch(input); err == nil { t.Fatal("ambiguous timing accepted") }
}
```

- [ ] **Step 2: 运行模型测试并确认 RED**

Run: `go test ./internal/videogen -run 'TestRepository(PersistsOrdered|RejectsAmbiguous)' -count=1 -v`

- [ ] **Step 3: 实现领域类型与一 Batch 一文件 Repository**

```go
type TimingInput struct { Mode string `json:"mode"`; VideoFrames int `json:"video_frames,omitempty"`; DurationSeconds float64 `json:"duration_seconds,omitempty"`; FPS int `json:"fps"` }
type SelectedAsset struct { AssetID string `json:"asset_id"`; Role string `json:"role"`; Order int `json:"order"` }
type Item struct {
    ID string; Order int; Prompt, NegativePrompt string; Enabled bool
    ParamsOverride json.RawMessage; TimingOverride *TimingInput
    InitImageID, EndImageID string; ControlFrameIDs []string; SelectedAssets []SelectedAsset
    Attempts []Attempt; CreatedAt, UpdatedAt time.Time
}
type Batch struct {
    ID, Folder, Title, PresetID string; ExecutionKind videoconfig.ExecutionKind; CommonParams json.RawMessage
    Timing TimingInput; Concurrency int; Items []Item; CreatedAt, UpdatedAt time.Time
}
type AssetSnapshot struct {
    ID, SHA256, MediaType, DisplayName, Role string; Size int64; Order int
}
type ResolvedTiming struct {
    InputMode string `json:"input_mode"`; DurationSeconds float64 `json:"duration_seconds,omitempty"`
    FPS int `json:"fps"`; RequestedFrames int `json:"requested_frames"`; AlgorithmVersion string `json:"algorithm_version"`
}
type Snapshot struct {
    ExecutionKind videoconfig.ExecutionKind `json:"execution_kind"`
    HTTPProvider *videoconfig.HTTPProvider `json:"http_provider,omitempty"`
    CLIPreset *videoconfig.CLIPreset `json:"cli_preset,omitempty"`
    Params json.RawMessage `json:"params"`; Prompt, NegativePrompt string
    Timing ResolvedTiming `json:"timing"`; InputAssets []AssetSnapshot `json:"input_assets"`
    CreatedAt time.Time `json:"created_at"`
}
type AttemptError struct { Code string `json:"code,omitempty"`; Message string `json:"message,omitempty"` }
type Attempt struct {
    ID, BatchID, ItemID string; ExecutionKind videoconfig.ExecutionKind; State AttemptState; Snapshot Snapshot
    RemoteJobID, RemoteStatus string; QueuePosition, PID, ActualFrameCount int
    WorkspaceRelativePath, OutputAssetID string; Error AttemptError
    CreatedAt time.Time; StartedAt, CompletedAt *time.Time
}
```

所有字段使用 snake_case JSON Tag。持久化为 `<root>/<32-hex-id>/batch.json`、Schema 1、0600 原子 JSON。Repository 提供 `CreateBatch/List/Get/UpdateBatch/DeleteBatch/CreateItems/UpdateItem/DeleteItem/MoveItem/CreateAttempt/UpdateAttempt/AttachResult`；所有返回深拷贝。Common/Override 必须是单个 JSON Object，并拒绝工作台管理的媒体、Prompt、FPS 和 `video_frames` 顶层键。

- [ ] **Step 4: 写失败的不可变快照、状态转换和重启恢复测试**

```go
func TestRepositoryInterruptsPersistedActiveAttemptsOnOpen(t *testing.T) {
    root := t.TempDir(); repository, batchID, itemID := attemptRepositoryFixture(t, root)
    attempt, _ := repository.CreateAttempt(batchID,itemID,CreateAttemptInput{State:AttemptQueued,Snapshot:validVideoSnapshot()})
    _, _ = repository.UpdateAttempt(batchID,itemID,attempt.ID,UpdateAttemptInput{State:AttemptRunning,PID:1234,WorkspaceRelativePath:attempt.ID})
    reopened, err := OpenRepository(root)
    if err != nil { t.Fatal(err) }
    got, _ := reopened.Get(batchID)
    if got.Items[0].Attempts[0].State != AttemptInterrupted { t.Fatal(got) }
}
```

状态为 `queued/submitting/polling/running/succeeded/failed/cancelled/interrupted`。只允许计划规定的前向转换；任何活动状态在 Open 时转为 interrupted；终态不可变。Snapshot 包含脱敏的 HTTP Provider 或 CLI Preset、合并参数、Timing 算法版本、请求帧数、FPS、输入 Asset 元数据/角色/顺序，不保存 Base64 或绝对 Asset 文件路径。

- [ ] **Step 5: 实现状态机、恢复与深拷贝**

```go
func activeAttemptState(state AttemptState) bool {
    return state == AttemptQueued || state == AttemptSubmitting || state == AttemptPolling || state == AttemptRunning
}

func terminalAttemptState(state AttemptState) bool {
    return state == AttemptSucceeded || state == AttemptFailed || state == AttemptCancelled || state == AttemptInterrupted
}
```

Repository 在每次变更时拥有时间戳；错误 Code/Message 分别限制 120/4096 字节；同一 Item 拒绝第二个活动 Attempt；结果 Asset ID 唯一。

- [ ] **Step 6: 验证并提交 Task 2**

Run: `gofmt -w internal/videogen && go test ./internal/videogen -count=1 && git diff --check`

```bash
git add internal/videogen
git commit -m "feat: persist video generation batches"
```

### Task 3: 视频输入与结果 Asset 引用一致性

**Files:**
- Create: `internal/videogen/service.go`
- Create: `internal/videogen/service_test.go`

**Interfaces:**
- Consumes: `*videogen.Repository`, `*asset.Repository`
- Produces: `NewService`, Batch/Item wrappers, `CreateAttempt`, `UpdateAttempt`, `AttachVideoResult`
- Reference modules: `video_item`, `video_attempt`, `video_result`

- [ ] **Step 1: 写失败的 active 输入、归档保留和顺序引用测试**

```go
func TestServiceAddsReferencesForEveryOrderedVideoInput(t *testing.T) {
    service, assets, batch := videoServiceFixture(t)
    init := importFixtureAsset(t, assets, "image/png")
    control := importFixtureAsset(t, assets, "image/png")
    ref := importFixtureAsset(t, assets, "video/webm")
    items, err := service.CreateItems(batch.ID, []CreateItemInput{{Prompt:"p",Enabled:true,InitImageID:init.ID,ControlFrameIDs:[]string{control.ID},SelectedAssets:[]SelectedAsset{{AssetID:ref.ID,Role:"reference_video",Order:0}}}})
    if err != nil { t.Fatal(err) }
    assertAssetReference(t, assets, init.ID, "video_item", items[0].ID, true)
    assertAssetReference(t, assets, control.ID, "video_item", items[0].ID, true)
    assertAssetReference(t, assets, ref.ID, "video_item", items[0].ID, true)
}
```

增加 `TestServiceRejectsArchivedNewSelection`、`TestServiceAllowsAlreadyReferencedArchivedInput`、`TestServiceRejectsNonImageHTTPFrames`、`TestServiceRollsBackReferenceFailure` 和删除 Item/Batch 释放所有输入/Attempt/结果引用测试。

- [ ] **Step 2: 运行 Service 测试并确认 RED**

Run: `go test ./internal/videogen -run 'TestService(Adds|Rejects|Allows|Rolls)' -count=1 -v`

- [ ] **Step 3: 实现引用 Diff、补偿与媒体角色校验**

```go
func (service *Service) validateNewInput(assetID, role string, requireImage bool) (asset.Asset, error) {
    item, ok := service.assets.Get(assetID)
    if !ok { return asset.Asset{}, ErrVideoAssetNotFound }
    if item.State != asset.StateActive { return asset.Asset{}, ErrVideoAssetNotActive }
    if requireImage && !strings.HasPrefix(item.MediaType, "image/") { return asset.Asset{}, ErrVideoAssetType }
    return item, nil
}
```

Init/End/Control 必须 `image/*`；CLI SelectedAssets 允许任意受控 Asset，但角色为 1–64 个安全字符并保持 Order。更新时先验证新引用，持久化后应用引用 Diff；失败恢复原 Batch 并逆序补偿 Asset 操作。

- [ ] **Step 4: 写失败的视频结果导入引用测试**

```go
func TestServiceAttachesArchiveVideoResult(t *testing.T) {
    service, assets, batchID, itemID, attemptID := videoResultFixture(t)
    result := importFixtureAsset(t, assets, "video/webm")
    got, err := service.AttachVideoResult(batchID,itemID,attemptID,result.ID)
    if err != nil || got.OutputAssetID != result.ID { t.Fatal(got, err) }
    assertAssetReference(t, assets, result.ID, "video_result", attemptID, true)
}
```

- [ ] **Step 5: 实现结果附加和删除清理**

`AttachVideoResult` 只接受 `video/*` 或 `image/webp` 动画容器结果，为 Attempt 添加唯一结果引用后写入 OutputAssetID；失败移除新引用。删除 Batch/Item 拒绝活动 Attempt，并释放输入快照和结果引用，但不删除 Asset 本体。

- [ ] **Step 6: 验证并提交 Task 3**

Run: `gofmt -w internal/videogen && go test ./internal/videogen -count=1 && git diff --check`

```bash
git add internal/videogen
git commit -m "feat: synchronize video assets"
```

### Task 4: 参数递归合并、时长换算和 HTTP 请求组装

**Files:**
- Create: `internal/videogen/params.go`
- Create: `internal/videogen/params_test.go`
- Create: `internal/videogen/http_assembler.go`
- Create: `internal/videogen/http_assembler_test.go`

**Interfaces:**
- Produces: `MergeParams(defaults,common,override json.RawMessage) (map[string]any,error)`
- Produces: `ResolveTiming(batch TimingInput,item *TimingInput) (ResolvedTiming,error)`
- Produces: `NewHTTPAssembler(assets)`, `BuildHTTP(batch,item,provider) (PreparedHTTP,Snapshot,error)`

- [ ] **Step 1: 写失败的递归 Merge、`null` 删除和 Timing 测试**

```go
func TestMergeParamsRecursesAndNullDeletesInheritedKeys(t *testing.T) {
    got, err := MergeParams(json.RawMessage(`{"sample_params":{"sample_steps":20,"eta":1},"lora":[1]}`), json.RawMessage(`{"sample_params":{"eta":0.5}}`), json.RawMessage(`{"sample_params":{"eta":null},"lora":[2]}`))
    if err != nil { t.Fatal(err) }
    sample := got["sample_params"].(map[string]any)
    if _, exists := sample["eta"]; exists || sample["sample_steps"].(json.Number).String() != "20" { t.Fatal(got) }
}

func TestResolveTimingUsesCeilingAndRecordsAlgorithm(t *testing.T) {
    got, err := ResolveTiming(TimingInput{Mode:"duration",DurationSeconds:2.01,FPS:16}, nil)
    if err != nil || got.RequestedFrames != 33 || got.AlgorithmVersion != "duration-ceil-v1" { t.Fatal(got, err) }
}
```

- [ ] **Step 2: 运行参数测试并确认 RED**

Run: `go test ./internal/videogen -run 'Test(MergeParams|ResolveTiming)' -count=1 -v`

- [ ] **Step 3: 实现严格 Object Merge 和明确换算**

```go
// duration mode: requested_frames = max(1, int(math.Ceil(duration_seconds*float64(fps))))
// frames mode: requested_frames = video_frames
```

三层输入均用 `json.Decoder.UseNumber`，必须是单个 Object；Object 递归合并，数组/标量替换，`null` 删除继承键。顶层拒绝 `prompt`、`negative_prompt`、`init_image`、`end_image`、`control_frames`、`fps`、`video_frames` 和 `batch_count`。Frames 模式要求 1–100000 帧和 1–240 FPS；Duration 模式要求 0.001–86400 秒和 1–240 FPS；不做 `4n+1` 修正。

- [ ] **Step 4: 写失败的 Asset Data URL、顺序和快照测试**

```go
func TestHTTPAssemblerInjectsFramesInOrderWithoutSnapshotBase64(t *testing.T) {
    assembler, assets, batch, item, provider := httpAssemblerFixture(t)
    first := importPNG(t, assets); second := importPNG(t, assets)
    item.ControlFrameIDs = []string{first.ID, second.ID}
    prepared, snapshot, err := assembler.BuildHTTP(batch,item,provider)
    if err != nil { t.Fatal(err) }
    if prepared.URL != provider.BaseURL+"/sdcpp/v1/vid_gen" || !bytes.Contains(prepared.Body, []byte(`"control_frames":["data:image/png;base64,`)) { t.Fatal(string(prepared.Body)) }
    encoded, _ := json.Marshal(snapshot)
    if bytes.Contains(encoded, []byte("base64")) || snapshot.InputAssets[0].ID != first.ID { t.Fatal(string(encoded)) }
}
```

增加输入总量、非图片、未引用 archive、敏感 Header 脱敏、Item Timing 覆盖、Managed Key 不能偷换媒体的测试。

- [ ] **Step 5: 实现 `BuildHTTP`**

```go
type PreparedHTTP struct {
    Body []byte; Provider videoconfig.HTTPProvider
    ConnectTimeout, JobTimeout, PollInterval time.Duration
}
```

读取 Init/End/Control 图片时逐个与累计限制检查，按原顺序编码 Data URL；最终 Body 注入 Prompt、Negative Prompt、FPS、请求帧数。Snapshot 保存脱敏 Provider、规范化合并参数、ResolvedTiming 和 Asset 的 ID/SHA256/MIME/Size/角色/顺序。

- [ ] **Step 6: 验证并提交 Task 4**

Run: `gofmt -w internal/videogen && go test ./internal/videogen -count=1 && git diff --check`

```bash
git add internal/videogen
git commit -m "feat: assemble native video requests"
```

### Task 5: stable-diffusion.cpp `vid_gen` 异步 Client

**Files:**
- Create: `internal/sdcpp/video_client.go`
- Create: `internal/sdcpp/video_client_test.go`

**Interfaces:**
- Produces: `VideoSubmission`, `VideoJob`, `VideoJobResult`, `VideoClient`
- Consumes: `videoconfig.HTTPProvider` and rendered Body

```go
func (VideoClient) Submit(context.Context,videoconfig.HTTPProvider,[]byte) (VideoSubmission,error)
func (VideoClient) Job(context.Context,videoconfig.HTTPProvider,string) (VideoJob,error)
func (VideoClient) Cancel(context.Context,videoconfig.HTTPProvider,string) error
```

- [ ] **Step 1: 写失败的提交与完成结果测试**

```go
func TestVideoClientSubmitsAndReadsCompletedVideoJob(t *testing.T) {
    server := httptest.NewServer(videoJobHandler(t, `{"id":"job-v","kind":"vid_gen","status":"completed","queue_position":0,"result":{"output_format":"webm","mime_type":"video/webm","fps":16,"frame_count":33,"b64_json":"R0tYZg=="},"error":null}`))
    defer server.Close()
    provider := testVideoProvider(server.URL)
    submitted, err := (VideoClient{}).Submit(context.Background(),provider,[]byte(`{"prompt":"cat"}`))
    if err != nil || submitted.Kind != "vid_gen" || submitted.PollURL != "/sdcpp/v1/jobs/job-v" { t.Fatal(submitted, err) }
    job, err := (VideoClient{}).Job(context.Background(),provider,"job-v")
    if err != nil || job.Result.FrameCount != 33 || job.Result.MIMEType != "video/webm" { t.Fatal(job, err) }
}
```

- [ ] **Step 2: 运行 Client 测试并确认 RED**

Run: `go test ./internal/sdcpp -run TestVideoClient -count=1 -v`

- [ ] **Step 3: 实现严格 DTO、固定路径和有界传输**

```go
type VideoJobResult struct { OutputFormat string `json:"output_format"`; MIMEType string `json:"mime_type"`; FPS int `json:"fps"`; FrameCount int `json:"frame_count"`; B64JSON string `json:"b64_json"` }
type VideoJob struct { ID, Kind, Status string; QueuePosition int; Result *VideoJobResult; Error *RemoteError }
```

Submit 只 POST 固定 `/sdcpp/v1/vid_gen` 并要求 202；Job/Cancel 只由 URL-escape 后的 Job ID 构造 `/sdcpp/v1/jobs/{id}`，不请求服务器返回的绝对 Poll URL。拒绝重定向；请求 Body 不得超过 MaxRequestBytes；Job JSON 读取上限为 `4*((MaxVideoBytes+2)/3)+(1<<20)`，错误正文单独限制 MaxErrorBytes；只接受 kind `vid_gen`、已知状态、非负队列位置。Completed 必须有唯一视频结果、允许格式/MIME、正 FPS/FrameCount；活动态不得携带结果/错误。

- [ ] **Step 4: 写失败的取消、限制和不可取消语义测试**

```go
func TestVideoClientReturnsTypedGeneratingCancelConflict(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) { w.WriteHeader(http.StatusConflict); io.WriteString(w,`{"error":"cannot_cancel_generating"}`) }))
    defer server.Close()
    err := (VideoClient{}).Cancel(context.Background(),testVideoProvider(server.URL),"job-v")
    var httpErr *HTTPError
    if !errors.As(err,&httpErr) || httpErr.StatusCode != http.StatusConflict { t.Fatal(err) }
}
```

增加 404/410、超限 Body/响应/错误、非法 JSON、跨源重定向和 Context 取消测试。

- [ ] **Step 5: 实现 Cancel 和共享 HTTP Helper 的最小提取**

把图像 Client 中可复用的 `requestJSON/readBounded/HTTPError` 保留在 `internal/sdcpp`，不得改变既有图像语义。Video Cancel 只接受 200；Manager 决定 409 后继续轮询。

- [ ] **Step 6: 验证并提交 Task 5**

Run: `gofmt -w internal/sdcpp && go test -race ./internal/sdcpp -count=1 && git diff --check`

```bash
git add internal/sdcpp
git commit -m "feat: call native video job API"
```

### Task 6: CLI 固定工作区、Manifest 和安全模板

**Files:**
- Create: `internal/videogen/workspace.go`
- Create: `internal/videogen/workspace_test.go`
- Create: `internal/videogen/template.go`
- Create: `internal/videogen/template_test.go`

**Interfaces:**
- Produces: `NewWorkspaceManager(root,assets)`, `Prepare(attemptID string,snapshot Snapshot) (Workspace,error)`, `Cleanup(attemptID string) error`
- Produces: `ExpandCLITemplate(template string,variables TemplateVariables) (string,error)`

- [ ] **Step 1: 写失败的只暂存已选 Asset 和硬链接回退测试**

```go
func TestWorkspacePreparesOrderedSelectedAssetsAndManifest(t *testing.T) {
    manager, assets := workspaceFixture(t)
    first := importFixtureAsset(t,assets,"video/webm"); second := importFixtureAsset(t,assets,"image/png")
    snapshot := validCLISnapshot([]AssetSnapshot{{ID:first.ID,Role:"reference_video",Order:0},{ID:second.ID,Role:"reference_image",Order:1}})
    workspace, err := manager.Prepare("0123456789abcdef0123456789abcdef",snapshot)
    if err != nil { t.Fatal(err) }
    if filepath.Base(workspace.Inputs[0].Path) != "000-reference-video.webm" || filepath.Base(workspace.Inputs[1].Path) != "001-reference-image.png" { t.Fatal(workspace.Inputs) }
    assertManifestHashesAndRelativePaths(t,workspace.ManifestPath,snapshot.InputAssets)
}
```

注入 Link 函数以确定性覆盖 `EXDEV` 后复制；验证复制内容哈希。增加拒绝不安全 Attempt ID、重复 Order、路径逃逸、未引用 archive、Manifest 绝对路径和 Cleanup 越界的测试。

- [ ] **Step 2: 运行 Workspace 测试并确认 RED**

Run: `go test ./internal/videogen -run TestWorkspace -count=1 -v`

- [ ] **Step 3: 实现固定目录与 Manifest**

```go
type Workspace struct { Root, InputDir, OutputDir, ManifestPath, OutputPath string; Inputs []StagedInput }
type Manifest struct { SchemaVersion int `json:"schema_version"`; AttemptID string `json:"attempt_id"`; Inputs []ManifestInput `json:"inputs"` }
```

Root 固定 `<data-dir>/video-workspace/<attempt-id>`，0700；Manifest 0600 原子写。只从 `asset.Repository.OpenContent` 获取源；先 `os.Link`，仅在跨文件系统/不支持时用 `io.Copy`，记录 `hardlink|copy`。文件名为三位序号、安全角色和受控扩展名。Cleanup 通过 `filepath.Rel`、父目录核对和 32-hex Attempt ID 三重确认后只删除该 Attempt 目录。

- [ ] **Step 4: 写失败的 Shell 引用和 RAW 变量测试**

```go
func TestExpandCLITemplateQuotesOrdinaryVariablesAndAllowsExplicitRaw(t *testing.T) {
    got, err := ExpandCLITemplate(`sd-cli -p {{PROMPT}} {{EXTRA_ARGS_RAW}} -o {{OUTPUT_PATH}}`, TemplateVariables{Values:map[string]string{"PROMPT":"a' b","OUTPUT_PATH":"/tmp/out.webm"},Raw:map[string]string{"EXTRA_ARGS_RAW":"--seed 7"}})
    if err != nil { t.Fatal(err) }
    if got != `sd-cli -p 'a'"'"' b' --seed 7 -o '/tmp/out.webm'` { t.Fatal(got) }
}
```

- [ ] **Step 5: 实现白名单模板变量**

只接受规格列出的变量；普通变量用单引号 Shell 安全转义；只有名称以 `_RAW` 结尾且调用者放入 Raw Map 时原样替换。未知、缺失、重复嵌套模板和超过 256 KiB 的结果返回错误。`CONTROL_FRAMES_JSON`、`SELECTED_ASSETS_JSON` 是 Workspace 相对清单的紧凑 JSON 字符串，再作为单参数引用。

- [ ] **Step 6: 验证并提交 Task 6**

Run: `gofmt -w internal/videogen && go test ./internal/videogen -count=1 && git diff --check`

```bash
git add internal/videogen
git commit -m "feat: prepare video cli workspaces"
```

### Task 7: 本机 CLI 进程组、有限日志和精确输出

**Files:**
- Create: `internal/videogen/process_linux.go`
- Create: `internal/videogen/process_linux_test.go`
- Create: `internal/videogen/log_buffer.go`
- Create: `internal/videogen/log_buffer_test.go`

**Interfaces:**
- Produces: `NewCLIExecutor()`, `Run(context.Context,CLIRunRequest) (CLIRunResult,error)`, `Stop(context.Context,string) error`
- Produces: `SnapshotLog`, `SubscribeLog`, `SaveLog`

- [ ] **Step 1: 写失败的准备/主命令顺序和进程组取消测试**

```go
func TestCLIExecutorRunsPrepareThenMainAndStopsProcessGroup(t *testing.T) {
    executor := NewCLIExecutor()
    request := fixtureCLIRunRequest(t, `printf prepared > "$OUTPUT_DIR/prepared"`, `test -f "$OUTPUT_DIR/prepared"; trap 'exit 0' TERM; while :; do sleep 1; done`)
    started := make(chan CLIRunResult,1)
    go func(){ result, _ := executor.Run(context.Background(),request); started <- result }()
    runID := waitForCLIProcess(t,executor)
    if err := executor.Stop(context.Background(),runID); err != nil { t.Fatal(err) }
    if result := <-started; result.ExitCode != 0 { t.Fatal(result) }
}
```

增加准备失败不运行主命令、Timeout、TERM 宽限后 KILL 整组、stdout/stderr 原样合并、慢订阅者不阻塞、同一 Attempt 只运行一次和 Shutdown 回收测试。

- [ ] **Step 2: 运行进程测试并确认 RED**

Run: `go test ./internal/videogen -run 'TestCLIExecutor|TestVideoLogBuffer' -count=1 -v`

- [ ] **Step 3: 实现 `/bin/bash -lc` 进程与日志**

```go
command := exec.CommandContext(context.Background(), "/bin/bash", "-lc", expanded)
command.Dir = request.WorkDir
command.Env = append(os.Environ(), sortedEnvironment(request.Env)...)
command.SysProcAttr = &syscall.SysProcAttr{Setpgid:true, Pdeathsig:syscall.SIGTERM}
command.Stdout, command.Stderr = logBuffer, logBuffer
```

准备命令与主命令分别获得独立进程组但共用 Attempt 日志；Context/Stop 对当前进程组 TERM→宽限→KILL。日志使用有界字节环、绝对偏移和原子 Snapshot+Subscribe；不自动落盘。Executor 的 Map 以 Attempt ID 为键，所有状态深拷贝。

- [ ] **Step 4: 写失败的精确输出路径、格式和上限测试**

```go
func TestCLIExecutorAcceptsOnlyDeclaredOutputInsideWorkspace(t *testing.T) {
    request := fixtureCLIRunRequest(t,"",`printf '\x1aE\xdf\xa3' > "$OUTPUT_PATH"`)
    request.OutputMediaType = "video/webm"; request.MaxOutputBytes = 1<<20
    result, err := NewCLIExecutor().Run(context.Background(),request)
    if err != nil || result.OutputPath != request.OutputPath { t.Fatal(result,err) }
}
```

增加命令成功但无输出、空文件、输出 Symlink、输出越过 `outputs/`、超限、扩展/MIME/魔数不匹配测试。HTTP 结果允许 WebM EBML、RIFF AVI、RIFF WEBP；CLI 额外允许 ISO BMFF `ftyp` 的 MP4/MOV，因为自定义命令的容器不受官方 HTTP 输出列表限制。

- [ ] **Step 5: 实现输出验证和手动保存日志**

用 `Lstat` 拒绝 Symlink，`filepath.Rel` 限制 outputs，`io.LimitReader` 验证上限和魔数。`SaveLog(attemptID,destination)` 只在用户调用时以 0600 原子写；正常/失败/Crash 都不自动保存。

- [ ] **Step 6: Race 验证并提交 Task 7**

Run: `gofmt -w internal/videogen && go test -race ./internal/videogen -count=1 && git diff --check`

```bash
git add internal/videogen
git commit -m "feat: execute local video cli jobs"
```

### Task 8: HTTP/CLI 统一调度、结果导入、取消和事件

**Files:**
- Create: `internal/videogen/manager.go`
- Create: `internal/videogen/manager_test.go`

**Interfaces:**
- Consumes: `*config.Repository`, `*Service`, `*HTTPAssembler`, `VideoRemoteClient`, `*WorkspaceManager`, `*CLIExecutor`, `*asset.Repository`
- Produces: `NewManager`, `StartBatch`, `StartItem`, `Retry`, `Cancel`, `GetAttempt`, `SubscribeBatch`, `SaveCLILog`, `CleanupWorkspace`, `Shutdown`

```go
type VideoRemoteClient interface {
    Submit(context.Context,videoconfig.HTTPProvider,[]byte) (sdcpp.VideoSubmission,error)
    Job(context.Context,videoconfig.HTTPProvider,string) (sdcpp.VideoJob,error)
    Cancel(context.Context,videoconfig.HTTPProvider,string) error
}
func NewManager(*config.Repository,*Service,*HTTPAssembler,VideoRemoteClient,*WorkspaceManager,*CLIExecutor,*asset.Repository) *Manager
func (m *Manager) StartBatch(batchID string) ([]Attempt,error)
func (m *Manager) StartItem(batchID,itemID string) (Attempt,error)
func (m *Manager) Retry(attemptID string) (Attempt,error)
func (m *Manager) Cancel(attemptID string) error
func (m *Manager) GetAttempt(attemptID string) (Attempt,bool)
func (m *Manager) SubscribeBatch(batchID string) (<-chan AttemptEvent,func(),error)
func (m *Manager) SaveCLILog(attemptID string) (string,error)
func (m *Manager) CleanupWorkspace(attemptID string) error
func (m *Manager) Shutdown(context.Context) error
```

- [ ] **Step 1: 写失败的预设共享并发和 Item 单活动测试**

```go
func TestManagerSharesPresetConcurrencyAcrossVideoBatches(t *testing.T) {
    fixture := newVideoManagerFixture(t,"http",1)
    first := fixture.createBatch("one"); second := fixture.createBatch("two")
    a, _ := fixture.manager.StartBatch(first.ID); b, _ := fixture.manager.StartBatch(second.ID)
    fixture.remote.releaseAll(); fixture.waitTerminal(append(a,b...))
    if fixture.remote.maximumActive() != 1 { t.Fatal(fixture.remote.maximumActive()) }
}
```

增加 Batch 更低并发、禁用/缺失预设、同 Item 活动冲突、Disabled Item 跳过、顺序执行和 Manager 关闭后拒绝新任务测试。

- [ ] **Step 2: 运行调度测试并确认 RED**

Run: `go test ./internal/videogen -run 'TestManager(Shares|Rejects|Skips|Runs)' -count=1 -v`

- [ ] **Step 3: 实现不可变预检和双执行路径调度**

Start 在外部调用前解析最新预设、合并参数、解析 Timing、校验/快照 Asset 并创建 queued Attempt。HTTP 使用 Provider/Preset 两级可调整 Semaphore；CLI 使用 CLIPreset/Preset 两级 Semaphore。每 Batch Queue 保持 Item Order；同一 Item 有活动 Attempt 时拒绝 Retry。

```go
switch attempt.ExecutionKind {
case videoconfig.ExecutionHTTP:
    manager.runHTTP(run)
case videoconfig.ExecutionLocalCLI:
    manager.runCLI(run)
default:
    manager.fail(run,"unsupported_execution","unsupported video execution kind")
}
```

- [ ] **Step 4: 写失败的 HTTP 轮询、实际帧数和不可取消测试**

```go
func TestManagerImportsHTTPVideoAndRecordsActualFrames(t *testing.T) {
    fixture := newVideoManagerFixture(t,"http",1)
    fixture.remote.completeVideo(validWebM(),"video/webm",16,33)
    attempt := fixture.startOneAndWait()
    if attempt.State != AttemptSucceeded || attempt.ActualFrameCount != 33 || attempt.OutputAssetID == "" { t.Fatal(attempt) }
    stored, _ := fixture.assets.Get(attempt.OutputAssetID)
    if stored.State != asset.StateArchive || stored.Source != "videogen:"+attempt.ID { t.Fatal(stored) }
}
```

增加 Poll 状态/队列位置持久化、严格 Base64、MaxVideoBytes、MIME/魔数、409 cancel 后继续轮询并显示 RemoteStatus、Queued cancel 不发远端、部分导入引用失败保留 Asset 的测试。

- [ ] **Step 5: 实现 HTTP 状态机和结果导入**

状态为 queued→submitting→polling→terminal。JobTimeout Context 覆盖提交/轮询；Timer 使用可取消 `time.Timer`。成功响应以 `base64.StdEncoding.Strict` 有界解码，验证 WebM/AVI/WebP 后立即 `asset.Import` 和 `AttachVideoResult`，记录响应 FPS/FrameCount。Cancel 409 时不写 cancelled，而发布“远端生成中，当前 Server 不支持中途取消”并继续轮询。

- [ ] **Step 6: 写失败的 CLI 工作区、取消、日志和结果导入测试**

```go
func TestManagerRunsCLIWorkspaceAndImportsDeclaredVideo(t *testing.T) {
    fixture := newVideoManagerFixture(t,"local_cli",1)
    attempt := fixture.startOneAndWait()
    if attempt.State != AttemptSucceeded || attempt.WorkspaceRelativePath != attempt.ID || attempt.OutputAssetID == "" { t.Fatal(attempt) }
    if !fixture.workspace.manifestContainsOnlySelected(attempt.ID) { t.Fatal("unexpected staged asset") }
}
```

增加进程组取消、Timeout、准备失败、日志 Snapshot/SSE、手动保存、工作区清理仅终态可用、CLI 结果格式错误和应用重启 interrupted 测试。

- [ ] **Step 7: 实现 CLI 状态机、事件与 Shutdown**

CLI queued→running→terminal；PID/Workspace 在启动后持久化。`SubscribeBatch` 先发送 Item Order 的最新 Attempt Snapshot，后续发送 state；`SubscribeLog` 使用字节偏移。Shutdown 先停止接收，取消 queued，停止 CLI 进程组；HTTP 已知 Job 尝试取消，409 只做有界等待后返回，不谎报 cancelled。

- [ ] **Step 8: 全量 Race 验证并提交 Task 8**

Run: `gofmt -w internal/videogen && go test -race ./internal/videogen ./internal/sdcpp -count=1 && git diff --check`

```bash
git add internal/videogen
git commit -m "feat: manage video generation attempts"
```

### Task 9: 外部尾帧提取记录与 Asset 导入

**Files:**
- Create: `internal/videogen/tail.go`
- Create: `internal/videogen/tail_repository.go`
- Create: `internal/videogen/tail_test.go`

**Interfaces:**
- Produces: `TailExtraction`, `TailExtractor`, `Extract`, `CancelExtraction`, `SubscribeExtraction`, `SaveExtractionLog`
- Persists: `<data-dir>/videos/tail-extractions.json` Schema 1

```go
func NewTailExtractor(*config.Repository,*TailRepository,*asset.Repository,*CLIExecutor,workspaceRoot,logRoot string) *TailExtractor
func (e *TailExtractor) Extract(context.Context,sourceAssetID,presetID string) (TailExtraction,error)
func (e *TailExtractor) CancelExtraction(context.Context,extractionID string) error
func (e *TailExtractor) SubscribeExtraction(extractionID string) (TailExtraction,<-chan TailExtraction,func(),error)
func (e *TailExtractor) SaveExtractionLog(extractionID string) (string,error)
func (e *TailExtractor) Shutdown(context.Context) error
```

- [ ] **Step 1: 写失败的提取成功、输入引用和重启中断测试**

```go
func TestTailExtractorImportsArchiveImageAndReferencesSource(t *testing.T) {
    extractor, assets := tailFixture(t, `printf '\x89PNG\r\n\x1a\n' > "$OUTPUT_IMAGE"`)
    source := importFixtureAsset(t,assets,"video/webm")
    extraction, err := extractor.Extract(context.Background(),source.ID,"tail-local")
    if err != nil { t.Fatal(err) }
    got := waitTailTerminal(t,extractor,extraction.ID)
    if got.State != AttemptSucceeded || got.OutputAssetID == "" { t.Fatal(got) }
    output, _ := assets.Get(got.OutputAssetID)
    if output.State != asset.StateArchive || output.Source != "video-tail:"+got.ID { t.Fatal(output) }
}
```

增加非视频/动画输入、禁用 Preset、命令失败、无输出、空文件、超限、非图片魔数、取消进程组、日志手动保存和 Repository Open 将活动记录转 interrupted 的测试。

- [ ] **Step 2: 运行尾帧测试并确认 RED**

Run: `go test ./internal/videogen -run TestTail -count=1 -v`

- [ ] **Step 3: 实现提取 Repository、模板和进程生命周期**

```go
type TailExtraction struct {
    ID, SourceAssetID, PresetID, State, OutputAssetID string
    PID int; Error AttemptError; CreatedAt time.Time; StartedAt,CompletedAt *time.Time
}
```

在外部命令前持久化 queued 记录并给源 Asset 添加 `video_attempt:<extraction-id>` 引用。临时目录固定 `<data-dir>/video-workspace/tail-<extraction-id>`；模板仅允许安全引用的 `INPUT_VIDEO`、`OUTPUT_IMAGE`、`ASSET_ID`。输出 Lstat 非 Symlink、大小合规并用 PNG/JPEG/WebP 魔数识别后导入 archive，再添加 `video_result:<extraction-id>`。失败不修改源 Asset；日志仅内存和手动保存。

- [ ] **Step 4: 验证并提交 Task 9**

Run: `gofmt -w internal/videogen && go test -race ./internal/videogen -count=1 && git diff --check`

```bash
git add internal/videogen
git commit -m "feat: extract video tail frames"
```

### Task 10: 视频配置、Batch、Attempt、CLI 与尾帧 Web API 和 App 生命周期

**Files:**
- Create: `internal/web/video_config.go`
- Create: `internal/web/video_config_test.go`
- Create: `internal/web/video_batch.go`
- Create: `internal/web/video_batch_test.go`
- Create: `internal/web/video_attempt.go`
- Create: `internal/web/video_attempt_test.go`
- Create: `internal/web/video_tail.go`
- Create: `internal/web/video_tail_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Produces: `/api/v1/videos/config`、`/capabilities`、Batch/Item CRUD、运行/重试/取消、Batch SSE、CLI 日志/保存/清理、尾帧创建/查询/取消/SSE
- App owns `videogen.Manager` and `TailExtractor` and shuts both down

- [ ] **Step 1: 写失败的完整配置和 Capabilities API 测试**

```go
func TestVideoConfigAPIReplacesAllPresetKinds(t *testing.T) {
    handler, repository := videoConfigFixture(t)
    body := marshalVideoConfigFixture(t)
    response := performJSON(handler,http.MethodPut,"/api/v1/videos/config",body)
    if response.Code != http.StatusOK || len(repository.Snapshot().Videos.CLIPresets) != 1 { t.Fatal(response.Body.String()) }
}
```

覆盖 GET、unknown field、无效 CLI 输出路径、禁用/缺失 HTTP Provider 和 mode-aware `vid_gen` Capabilities。上游 timeout→504，非 2xx→502，配置错误→400/404。

- [ ] **Step 2: 写失败的 Batch/Item CRUD 和执行 API 测试**

覆盖搜索/文件夹 List、Batch POST/GET/PUT/DELETE、Item 批量创建/更新/删除/排序、Batch Execute 202、Item Retry 202、活动冲突 409、Attempt GET/Cancel、SSE Snapshot/State/heartbeat。Batch Detail 返回预设可用性和引用 Asset 摘要，但不返回 Base64 或本地受控文件绝对路径。

- [ ] **Step 3: 写失败的 CLI 日志、工作区和尾帧 API 测试**

```go
func TestVideoCLILogSSEUsesRawByteOffsets(t *testing.T) {
    handler := videoAPIFixture(t)
    response := openVideoLogStream(t,handler,"attempt-a")
    response.waitFor(t,"event: snapshot")
    if !strings.Contains(response.String(),`"data_base64"`) || !strings.Contains(response.String(),`"start_offset"`) { t.Fatal(response.String()) }
}
```

覆盖手动保存返回工作台路径、浏览器清屏无服务端 API、Cleanup 活动态 409、Tail POST 202、Tail GET/Cancel/SSE 和精选结果仍通过现有 Asset State API 完成。

- [ ] **Step 4: 实现严格 Handler 与错误映射**

所有写入使用 `decodeStrictJSON`；ID 必须 32-hex；Create 返回 201/202，更新 200，删除 204。领域缺失→404，活动/并发冲突→409，验证→400，上游→502/504，存储/进程内部错误→500 且不泄露不必要的绝对路径。SSE 每 15 秒 heartbeat，Request Context 结束时取消订阅。

- [ ] **Step 5: 写失败的真实 App 组装、重启和关闭测试**

```go
func TestApplicationPersistsVideoBatchAndInterruptsActiveAttemptOnReopen(t *testing.T) {
    dataDir := t.TempDir()
    runtime := newApplicationFixture(t,dataDir)
    batchID, attemptID := createVideoAndStartFixture(t,runtime.server.Handler)
    shutdownRuntime(t,runtime)
    reopened := newApplicationFixture(t,dataDir)
    got := getVideoAttempt(t,reopened.server.Handler,batchID,attemptID)
    if got.State != videogen.AttemptInterrupted { t.Fatal(got) }
}
```

- [ ] **Step 6: 组装 Repository/Service/Manager/Tail 并加入关闭顺序**

打开 `videos/batches`、`videos/tail-extractions.json` 和 `video-workspace`；构造 VideoClient、Workspace、CLIExecutor、Manager、TailExtractor；传入 Web Options。`applicationRuntime` 增加 Video Manager/Tail，`shutdownManagers` 与 Image/LLM/Backend 一起并发调用并共享现有关闭 Context。

- [ ] **Step 7: Race 验证并提交 Task 10**

Run: `gofmt -w internal/web internal/app && go test -race ./internal/web ./internal/app ./internal/videogen -count=1 && git diff --check`

```bash
git add internal/web internal/app
git commit -m "feat: expose video workspace api"
```

### Task 11: 原生视频预设配置界面

**Files:**
- Create: `internal/web/static/video-config.js`
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/server.go`
- Modify: `internal/web/server_test.go`

**Interfaces:**
- Produces: `createVideoConfig({readAPIError})` with `enter()`/`leave()`
- Consumes: `GET/PUT /api/v1/videos/config` and Provider capabilities

- [ ] **Step 1: 写失败的嵌入资源和表单 Contract 测试**

断言设置页有 HTTP Provider、CLI Preset、Tail Frame Preset 三个紧凑列表和新建/复制/删除；HTTP 基础区含名称/URL/启用/并发，CLI 基础区含命令、工作目录、精确输出，Tail 含命令/扩展名；高级字段均在 `<details>`。断言 `/assets/video-config.js` 可访问并导出 `createVideoConfig`。

- [ ] **Step 2: 运行静态 Contract 并确认 RED**

Run: `go test ./internal/web -run TestEmbeddedVideoConfig -count=1 -v`

- [ ] **Step 3: 实现三类渐进式编辑器**

```js
export function createVideoConfig({ readAPIError }) {
  let loaded = false;
  return { async enter() { if (!loaded) await refresh(); }, leave() { closeEditors(); } };
}
```

保存始终 PUT 完整 Config；Header/Env/Default Params 是严格 Object JSON；数值范围先本地校验，服务端错误显示在表单旁。CLI 明示“仅本机执行”和 `_RAW` 风险；不展示 Remote Worker 选项。Capabilities 只显示 vid_gen 支持、输出格式和功能提示，不隐藏模型相关字段。

- [ ] **Step 4: 接入 Settings 生命周期和响应式样式**

`app.js` 只负责 enter/leave；领域状态都留在 `video-config.js`。桌面 Dialog 保持紧凑，720px 以下接近全屏；不增加图标、第三方脚本或构建步骤。

- [ ] **Step 5: 验证并提交 Task 11**

Run: `go test ./internal/web ./internal/app -count=1 && git diff --check`

```bash
git add internal/web
git commit -m "feat: configure video presets in browser"
```

### Task 12: 响应式视频 Batch、Item、Attempt 和尾帧工作区

**Files:**
- Create: `internal/web/static/videos.js`
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/server.go`
- Modify: `internal/web/server_test.go`

**Interfaces:**
- Produces: `createVideoWorkspace({openAssetPicker,readAPIError})` with `enter()`/`leave()`
- Consumes: Task 10 视频 API 和现有 active Asset Picker/Asset State API

- [ ] **Step 1: 写失败的页面结构和无依赖 Contract 测试**

断言顶部“视频”模块不再是占位；左栏有搜索/文件夹/Batch；右侧顶部有标题/文件夹/执行类型/预设/保存/运行；中部有 Timing 两种互斥模式、公共 JSON、批量 Prompt；Item 有 Init/End/Control/CLI 素材按钮；结果卡使用 `<video controls preload="metadata">`；快照、Job、CLI 日志/路径/历史和模型限制位于 `<details>`。

- [ ] **Step 2: 运行页面 Contract 并确认 RED**

Run: `go test ./internal/web -run TestEmbeddedVideoWorkspace -count=1 -v`

- [ ] **Step 3: 实现 Batch 列表、标题/文件夹和 Item 编辑**

`videos.js` 维护选中 Batch、草稿和 SSE 生命周期。切换 Batch 前保存明确按钮，不自动 AI 命名。Timing 切换只提交当前模式；Duration 即时显示 `ceil(duration*fps)` 请求帧数和“实际帧数由结果决定”。多行 Prompt 导入每个非空行创建一个 Item；排序/启用/复制/删除都有文字按钮。

- [ ] **Step 4: 接入 active Asset Picker 与有序角色**

Init/End/Control Picker 过滤 `image/`；CLI Picker 只展示 active 全类型，选择后要求角色并可上下排序。已引用后归档的 Asset 以“已归档但仍引用”显示；用户移除前保留。任何 Asset 内容都通过既有受控内容 API 预览，不把文件路径写入 DOM。

- [ ] **Step 5: 实现运行、SSE、CLI 日志和浏览器清屏**

Batch/Item 运行后立即渲染 queued Attempt；Batch SSE 更新状态。CLI 日志 SSE 使用 Base64 原始字节和权威偏移，浏览器清屏只记录本地 clear offset，不调用服务端 Clear；手动保存才调用 Save。取消 409 的远端生成提示必须直接显示。

- [ ] **Step 6: 实现结果卡、尾帧和历史折叠**

成功结果通过 Asset 内容 URL 填入原生 `<video>`；显示请求/实际帧数、FPS 和时长。尾帧按钮选择 Preset 并订阅提取状态；成功后提供“设为精选”调用现有 Asset State API，以及“作为首帧”写入当前/新 Item。CLI 终态显示清理工作区按钮，操作前浏览器确认。

- [ ] **Step 7: 完成响应式布局和生命周期清理**

桌面复用左右布局；720px 以下左栏抽屉、主区单列、视频宽度 100%。离开模块关闭 EventSource/Timer，不取消任务；重新进入通过 GET Snapshot 对账。所有复杂表格窄屏转卡片，不产生横向页面滚动。

- [ ] **Step 8: 验证并提交 Task 12**

Run: `go test ./internal/web ./internal/app -count=1 && git diff --check`

若 Node 存在再运行：`node --check internal/web/static/video-config.js && node --check internal/web/static/videos.js`

```bash
git add internal/web
git commit -m "feat: add responsive video workspace"
```

### Task 13: 文档、真实 CLI/HTTP 验收、阶段审查和推送

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md`
- Modify: `docs/superpowers/specs/2026-08-31-video-workspace-design.md`
- Modify: `docs/superpowers/plans/2026-08-31-video-workspace.md`

**Interfaces:**
- Consumes: Tasks 1–12 最终 API、页面、路径和限制
- Produces: 可复制的配置/运行说明、验收证据、已交付阶段状态和远端提交

- [ ] **Step 1: 更新用户文档和边界说明**

README 说明 HTTP Provider URL、CLI/Tail 模板、`<data-dir>/video-workspace`、精确 OUTPUT_PATH、`_RAW` 风险、手动日志保存、archive→active 流程，以及 Remote Worker 不执行视频 CLI/不传素材。链接官方 stable-diffusion.cpp Server API 和视频模型指南，不复制易过时的启动参数。

- [ ] **Step 2: 更新设计与计划状态**

总体设计把阶段 7 标为已交付；视频规格状态改为“已交付”并记录真实实现差异与限制；本计划只勾选已完成步骤，不修改规格迁就代码。

- [ ] **Step 3: 运行完整自动验证**

```bash
gofmt -w cmd internal
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build -o /tmp/ai-workbench-video-check ./cmd/ai-workbench
git diff --check
```

Expected: 全部退出 0，无 Race、Vet、构建或空白错误。

- [ ] **Step 4: 运行真实 HTTP 和 CLI Fixture 验收**

启动真实工作台临时数据目录和 `httptest` 等价的独立本机 Fixture Server：创建 HTTP Batch，确认 `/vid_gen` Body 的 Prompt/FPS/Frames/有序帧，轮询成功并导入可下载 Asset。配置一个只用 Shell 内建和 `printf` 生成最小 WebM 魔数的 CLI Preset，确认 Manifest 只含所选素材、日志实时显示、取消整组和精确输出导入。配置 Tail Fixture 生成最小 PNG，确认 archive 导入、精选和首帧复用。退出后确认无 Fixture 进程残留。

- [ ] **Step 5: 手工桌面/窄屏验收**

在 1440px 与 390px 宽度检查：左栏/抽屉、标题文件夹、Timing 互斥、Item 素材顺序、结果 `<video>`、折叠高级信息、直接可见错误、CLI 浏览器清屏和 Tail 操作。记录检查结果到视频规格交付记录。

- [ ] **Step 6: 审查需求映射和工作树**

```bash
rg -n 'T[O]DO|T[B]D|待确[认]|以后再[定]' README.md docs/superpowers/specs/2026-08-31-video-workspace-design.md docs/superpowers/plans/2026-08-31-video-workspace.md
git status --short
git diff --stat
```

Expected: 占位扫描无输出；状态只包含本 Task 文档；最终代码审查无 Critical/Important 未处理问题。

- [ ] **Step 7: 提交、推送并核对远端**

```bash
git add README.md docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md docs/superpowers/specs/2026-08-31-video-workspace-design.md docs/superpowers/plans/2026-08-31-video-workspace.md
git commit -m "docs: complete video workspace phase"
git push origin HEAD
git status --short --branch
git rev-parse HEAD
git rev-parse @{upstream}
```

Expected: 工作树干净，本地与上游提交哈希完全一致。
