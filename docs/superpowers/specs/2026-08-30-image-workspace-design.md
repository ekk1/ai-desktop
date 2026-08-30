# 生图批次工作区设计

**日期：** 2026-08-30

**状态：** 已批准，待实施

**上位设计：** `docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md`

## 1. 目标与范围

本阶段交付独立的生图工作区：用户配置一个或多个 stable-diffusion.cpp Server，维护带文件夹的图像批次，为多个有序提示词设置公共参数和单项覆盖，通过官方原生异步 API 提交、轮询和取消任务，并把每张成功图片导入共享 Asset 库。

本阶段包含：

- stable-diffusion.cpp 原生 `sdcpp` 图像 Provider 配置和 capabilities 读取。
- 图像批次、提示词 Item、不可变 Attempt 快照、重试和中断恢复。
- 公共参数、单项覆盖、批量提示词录入和键盘可用的上下移动。
- init image、ref images、mask、control image 和 IP Adapter image 的 Asset 引用。
- 有限并发调度、原生 Job 轮询、远端取消和本地关闭取消。
- Base64 结果校验、逐张导入共享 Asset、archive/active 调整。
- 原生响应式浏览器界面和渐进展开的完整 JSON 参数编辑。

本阶段不包含视频、图像 CLI、自动启动或切换后端 Profile、自动提示词扩写、图片编辑器、画布蒙版工具、跨 Provider 协议模板、WebUI/OpenAI 兼容接口或浏览器直连 stable-diffusion.cpp。Server 启停仍由现有“后端管理”模块负责。

## 2. 协议选择

默认且首版唯一执行协议是 stable-diffusion.cpp 官方原生异步接口：

- `GET /sdcpp/v1/capabilities`
- `POST /sdcpp/v1/img_gen`
- `GET /sdcpp/v1/jobs/{id}`
- `POST /sdcpp/v1/jobs/{id}/cancel`

原生接口提供显式参数、队列状态和取消能力，适合工作台自己的批次与恢复模型。同步 `/sdapi/v1/txt2img`、`/sdapi/v1/img2img` 和 `/v1/images/*` 不进入首版；以后若确有需求，应作为独立协议适配器加入，而不是在原生状态机中增加条件分支。

官方协议以 `https://github.com/leejet/stable-diffusion.cpp/blob/master/examples/server/api.md` 为准。Provider 只保存 Base URL 和传输限制，不保存 Server 启动参数；模型、GPU 和启动命令继续由后端 Profile 自行定义。

## 3. 领域边界与包结构

- `internal/sdcpp`：图像 Provider 配置、capabilities DTO、受限 HTTP Client、请求合并和原生 Job 协议。
- `internal/imagegen`：Batch/Item/Attempt 模型、持久化、Asset 引用 Service、调度 Manager 和事件订阅。
- `internal/config`：主配置 Schema 3，保存 `sdcpp.ImageConfig` 并提供运行时更新。
- `internal/web`：图像配置、批次、Item、Attempt、capabilities 和 SSE API。
- `internal/web/static/images.js`：生图模块全部浏览器状态、渲染和请求逻辑。
- `internal/asset`：继续负责结果内容存储、哈希去重、archive/active 和引用保护。

`imagegen` 不直接读取或改写 Asset JSON；所有输入和结果引用均通过 `asset.Repository`。`sdcpp` 不依赖 `config`、`imagegen` 或 `asset`，避免配置和执行层循环依赖。

## 4. 配置与迁移

主 `config.json` 从 Schema 2 迁移到 3，新增 `images`：

```json
{
  "providers": [
    {
      "id": "sdcpp-local",
      "name": "stable-diffusion.cpp Local",
      "base_url": "http://127.0.0.1:1234",
      "headers": {},
      "connect_timeout_seconds": 10,
      "job_timeout_seconds": 3600,
      "poll_interval_milliseconds": 750,
      "max_response_bytes": 268435456,
      "max_image_bytes": 134217728,
      "max_concurrent_jobs": 1,
      "enabled": true
    }
  ]
}
```

约束：

- ID 稳定且唯一；Base URL 必须是绝对 HTTP(S) URL，存储时去除尾部 `/`。
- Header 名和值拒绝 CR/LF；首版没有 Header 模板或隐藏密钥语义，用户可录入固定 Header。
- 连接超时范围 1–300 秒；Job 总超时范围 1–86400 秒。
- 轮询间隔范围 100–10000 毫秒；响应和单图限制范围 1 Byte–1 GiB。
- Provider 最大并发范围 1–16；禁用 Provider 不能启动新 Attempt。
- Schema 2→3 保留原有配置并加入上述本地默认值。

配置保存后影响新 Attempt；已排队和运行 Attempt 使用自身不可变 Provider 快照。API Key 若被放进固定 Header，会保存在本机 `0600` 主配置中；Attempt 快照按敏感 Header 名把值替换为 `<redacted>`。

## 5. Batch、Item 与 Attempt

### 5.1 Batch

Batch 包含 ID、用户标题、文件夹、Provider ID、并发数、BaseParams JSON Object、Items、创建和更新时间。并发数不能超过 Provider 的 `max_concurrent_jobs`。左栏文件夹由 Batch 字段派生，不维护独立索引。

BaseParams 保存官方原生 `img_gen` 请求的公共参数，但不保存 prompt、negative_prompt 或图像数据。新 Batch 使用精简默认值：宽高 1024、seed -1、batch_count 1、PNG 输出；sampling 等未填写字段保持省略，让当前 Server 默认值生效。用户可点击“读取 capabilities”把当前 Server 的 `defaults_by_mode.img_gen` 明确复制到草稿。

### 5.2 Item

Item 包含 ID、Order、Prompt、NegativePrompt、ParamsOverride JSON Object、InputAssets、Attempts、创建和更新时间。`InputAssets` 字段为：

- `init_image_id`
- `ref_image_ids`
- `mask_image_id`
- `control_image_id`
- `ip_adapter_image_id`

批量录入按非空行创建 Item；每行是一条 Prompt。Item 可单独编辑、复制、删除并使用“上移/下移”调整稳定顺序。任何已有 Attempt 的 Item 仍可修改；旧 Attempt 快照不变。

### 5.3 Attempt

每次首次运行或重试创建新 Attempt，旧 Attempt 永不覆盖。字段包括：

- ID、Item ID 和状态。
- ProviderSnapshot、ParamsSnapshot 和 InputAssetSnapshots。
- RemoteJobID、远端状态和 QueuePosition。
- ResultAssetIDs、有限错误码/说明、创建/开始/完成时间。

状态为 `queued`、`submitting`、`polling`、`succeeded`、`failed`、`cancelled` 和 `interrupted`。Attempt 必须在任何外部 HTTP 前持久化。重启时遗留的 queued/submitting/polling Attempt 标记为 interrupted，不自动轮询、取消或重新提交远端 Job。

## 6. 请求合并和参数完整性

所有 JSON 参数都是单个 Object。请求按以下确定顺序构建：

1. 深拷贝 Batch BaseParams。
2. 递归合并 Item ParamsOverride；Object 递归合并，scalar/array/null 整体替换。
3. 强制写入 Item Prompt 和 NegativePrompt。
4. 从受控 Asset 内容生成 Data URL，强制写入五类图像字段。

以下键为工作台管理键，BaseParams 和 ParamsOverride 中禁止出现：`prompt`、`negative_prompt`、`init_image`、`ref_images`、`mask_image`、`control_image`、`ip_adapter_image`。这避免 JSON 覆盖绕过 Item 和 Asset 引用；这些字段全部在常用表单或 Asset Picker 中可表达。

除此以外不建立字段白名单。`sample_params`、`guidance`、LoRA、hires、VAE tiling、cache、SCM、输出格式以及 stable-diffusion.cpp 后续新增字段均可通过 JSON Object 原样发送，因此高级参数不会被工作台版本限制。界面常用字段只是对同一 JSON 草稿的便利编辑，不产生第二套参数来源。

组装时要求所有引用 Asset 存在且媒体类型为 `image/*`，并在 Provider `max_image_bytes` 总预算内。Snapshot 记录 Asset ID、SHA-256、媒体类型、尺寸和大小，不持久化 Base64/Data URL；发送体只在内存中存在。

## 7. capabilities 与常用参数

capabilities 是辅助信息，不是执行前置条件。Server 不可达时，用户仍可编辑和保存批次。

界面读取并使用 mode-aware 字段：

- `supported_modes` 必须包含 `img_gen` 才显示当前模型可执行。
- `defaults_by_mode.img_gen` 用于显式载入默认参数。
- `features_by_mode.img_gen` 控制图像引用、LoRA、hires、cache 和取消能力提示。
- `samplers`、`schedulers`、`loras`、`upscalers` 用于原生 datalist/select 建议，但仍允许手工值。
- `limits` 用于表单提示和提交前校验宽高、batch_count；Server 仍是最终校验方。

默认表单展示 Prompt、Negative Prompt、width、height、seed、batch_count、sample steps、text CFG、sampler、scheduler 和输出格式。init/mask/control/IP Adapter/refs 使用 Asset Picker。LoRA、hires、VAE tiling和其余字段在“完整参数 JSON”折叠区编辑。

## 8. Asset 引用与结果导入

输入引用使用 `asset.Reference{Module:"image_item", RecordID:<item-id>}`。选择器只显示 active Asset；已引用后归档的图片继续保留，直到用户明确移除。删除 Item 或 Batch 时同步移除输入引用。

完成 Job 的 `result.images[].b64_json` 逐项处理：

1. 受 Provider 响应总大小限制读取 Job JSON。
2. 对每项执行严格标准 Base64 解码和单图大小限制。
3. 根据 `result.output_format` 与内容嗅探确定 `image/png`、`image/jpeg` 或 `image/webp`；格式不匹配时拒绝该项。
4. 调用 `asset.Repository.Import`，DisplayName 包含 Batch、Item、Attempt 和结果序号，Source 为 `imagegen:<attempt-id>`。
5. 新 Asset 默认 archive，并添加 `asset.Reference{Module:"image_attempt", RecordID:<attempt-id>}`。

逐张导入允许部分成功。任一结果失败时 Attempt 为 failed，保留已成功的 ResultAssetIDs 和错误说明。用户可在批次结果区把结果切换为 active、打开 Gallery 详情，或明确从 Attempt 移除引用；移除引用不物理删除 Asset。

## 9. 调度、轮询和取消

Manager 为每个启用 Provider 建立共享信号量，保证跨 Batch 的活动远端 Job 不超过 `max_concurrent_jobs`。Batch 的并发设置是更低的局部上限。启动“运行待处理项”会按 Item Order 为没有活动 Attempt 的 Item 创建 queued Attempt；单项运行和重试走同一队列。

Worker 流程：

1. 取得 Provider 和 Batch 并发额度。
2. 持久化 submitting 和完整 Snapshot。
3. 组装内存请求并 `POST /sdcpp/v1/img_gen`，要求 202 和有效 Job ID。
4. 持久化 RemoteJobID，进入 polling。
5. 按 Provider 间隔 GET Job，更新远端状态和队列位置。
6. completed 时导入全部结果；failed/cancelled 映射到本地终态。

取消 queued Attempt 只更新本地状态；有 RemoteJobID 时向官方 cancel endpoint 发出一次有界请求。404/409/410 后再读取一次 Job：若已完成则导入结果，否则保存有限错误并结束。浏览器断开不取消任务；应用正常关闭停止接收新任务、取消 Context、对已提交 Job 尝试远端取消，并等待 Manager 退出。

每次状态改变通过订阅器发布。浏览器使用工作台自己的 SSE，不直接轮询 stable-diffusion.cpp。慢订阅者只保证收到最新状态；所有权威状态均已持久化，可重新 GET Batch 恢复。

## 10. 持久化与恢复

```text
<data-dir>/images/
└── batches/
    └── <batch-id>/
        └── batch.json
```

每个 Batch 一个带 `schema_version` 的原子 JSON 文档，包含 Item 和 Attempt 元数据，不包含图片 Base64。Repository 启动时扫描程序生成的 32-hex 目录，验证 ID、顺序、状态、Provider 引用以外的结构约束并返回深拷贝。

外部配置删除不会阻止旧 Batch 加载；只会阻止使用缺失 Provider 创建新 Attempt。运行时删除 Batch 必须先拒绝或取消活动 Attempt，再同步释放所有 `image_item` 和 `image_attempt` Asset 引用，最后删除受控 Batch 目录。任一步失败时保留可恢复文档并返回错误。

## 11. HTTP API

### 配置和 capabilities

- `GET/PUT /api/v1/images/config`
- `GET /api/v1/images/providers/{provider-id}/capabilities`

### Batch 和 Item

- `GET/POST /api/v1/images/batches`
- `GET/PUT/DELETE /api/v1/images/batches/{batch-id}`
- `POST /api/v1/images/batches/{batch-id}/items`
- `PUT/DELETE /api/v1/images/batches/{batch-id}/items/{item-id}`
- `POST /api/v1/images/batches/{batch-id}/items/{item-id}/move`

批量 Item POST 接受 `items` 数组而不是隐式解析多行文本；换行解析仅发生在浏览器。所有写 API 严格解码单个 JSON Object 并限制 Body。

### 执行

- `POST /api/v1/images/batches/{batch-id}/execute`
- `POST /api/v1/images/batches/{batch-id}/items/{item-id}/execute`
- `POST /api/v1/images/attempts/{attempt-id}/cancel`
- `GET /api/v1/images/attempts/{attempt-id}`
- `GET /api/v1/images/batches/{batch-id}/events`

执行 API 返回创建的 Attempt 数组。Batch GET 返回完整 Batch、当前 Provider 可用性和关联 Asset 摘要；浏览器不自行重建领域状态。错误沿用稳定 Error Envelope，远端正文最多保留 4096 Bytes。

## 12. 浏览器界面

生图模块复用全局左栏：搜索、文件夹筛选、新建 Batch 和 Batch 列表。右侧由四块组成：

- Batch 工具栏：标题、文件夹、Provider、并发、保存、删除、读取 capabilities。
- 公共参数卡：常用字段默认展开，完整 BaseParams JSON、capabilities 和技术信息折叠。
- Item 列表：批量添加提示词、单项编辑、上移/下移、输入 Asset、单项 JSON 覆盖、运行/重试/取消。
- 结果区：响应式图片网格、Attempt 状态、错误、archive/active 切换和 Gallery 入口。

列表默认只显示 Prompt 摘要、状态和高频按钮；完整请求快照、远端 Job ID、队列位置、历史 Attempt 与技术错误使用 `<details>`。不实现拖拽。所有媒体渲染使用受控 `/api/v1/assets/{id}/content` URL，不把 Base64 放进 DOM。

≤760px 时左栏使用现有抽屉，工具栏和常用字段单列，Item 操作换行，参数 Dialog 接近全屏，结果网格降为单列或双列。所有 Dialog 使用原生 `<dialog>`，表单提交和关闭可通过键盘完成。

## 13. 错误和安全边界

- 所有 Provider 请求只由 Go Server 发出；浏览器只访问工作台 `/api/v1/`。
- URL 必须来自已保存 Provider，远端 `poll_url` 仅接受相对同源 sdcpp 路径；不跟随任意绝对 Job URL。
- HTTP 连接、整体 Job、响应正文、单图和输入图片总量都有硬上限。
- Base64 解码、媒体嗅探或 Asset 导入失败不会登记伪成功结果。
- 错误不包含固定敏感 Header；快照对常见认证 Header 脱敏。
- 本应用仍是无鉴权的本机单用户工具，能够访问页面的人可修改 Provider 并发起请求。

## 14. 测试与验收

全部自动化测试只使用 Go 标准库：

- Config：Schema 2→3、Provider 校验、深拷贝、运行时保存和 `0600`。
- Params：递归合并、保留未知字段、管理键拒绝、Asset 字段注入和快照不含 Base64。
- Repository：Batch/Item CRUD、稳定排序、Attempt 历史、中断恢复、重开持久化和深拷贝。
- Service：输入/结果 Asset 引用创建、替换、删除与失败补偿。
- Client：真实 `httptest.Server` capabilities、202 提交、轮询完成、失败、超限、超时和取消。
- Manager：跨 Batch Provider 并发、局部并发、批量顺序、重试、部分导入、SSE replay 和关闭等待。
- Web/App：严格 API、依赖装配、真实生命周期和嵌入式响应式 UI 契约。

验收不依赖真实模型或 GPU。HTTP Fixture 返回最小合法 PNG/JPEG/WebP Base64，验证每张结果成为 archive Asset。最终二进制冒烟测试创建 Batch、读取默认 Provider、加载生图工作区资源并正常关闭。
