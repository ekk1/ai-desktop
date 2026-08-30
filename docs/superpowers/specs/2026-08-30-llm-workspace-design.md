# LLM 完整请求工作区设计

**日期：** 2026-08-30

**状态：** 已批准，待实施

**上位设计：** `docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md`

## 1. 目标与范围

本阶段交付一个无聊天角色语义的 LLM 完整请求工作区：用户在文件夹和会话中维护可分支的 Panel 树，选择当前根到节点路径，组装不可变请求快照，并通过一个或多个快捷 Provider 执行。多个流式结果成为当前节点下互为兄弟的子 Panel；Exa 请求必须由用户从合法 JSON Panel 手动触发。

本阶段包含：

- 文件夹、会话标题、Panel 分支、修订和从任意节点派生新会话。
- Panel 内容、启用状态、知识条目和 Asset 引用。
- Prompt 模板、Provider、快捷路径和 Exa Key 的运行时配置。
- llama.cpp `/completion` 预设与通用 JSON HTTP/SSE Provider。
- 不可变请求快照、异步 Run、取消、SSE 浏览器增量和结果 Panel。
- 严格 Exa JSON 检测、手动执行和结果 Panel。
- 原生响应式 LLM 界面和配置界面。

不包含聊天角色、自动工具循环、自动联网、RAG、token 计数器、Provider SDK、JavaScript 模板或浏览器直连第三方 API。

## 2. 方案选择

采用“领域无关 Panel + 通用 HTTP 模板执行器 + 内置预设”。

- 不为 llama.cpp、OpenAI 兼容服务和在线 Provider 分别建立会话模型。Panel 永远只有普通内容和引用。
- Provider 保存 URL、Headers 模板、JSON Body 模板、同步/流式模式与 JSON 提取路径。llama.cpp 只是创建配置时可选的一组默认值。
- 工作台 Server 代理所有 Provider 请求，负责密钥、超时、取消、快照和流式解析；浏览器不持有第三方 Key。

未采用的方案：

- 为每种 Provider 硬编码适配器：初期表单更简单，但会持续追赶协议字段并把在线协议带入领域模型。
- 仅允许粘贴完整请求体、不提供预设：最灵活，但常用本地路径配置成本高，也难以稳定提取流式结果。
- 把每条 Panel 映射为 user/assistant/system：违背完整请求工作台的核心约束。

## 3. 数据目录

```text
data/
├── config.json                         # Provider、快捷路径、模板和 Exa Key
└── sessions/
    └── <session-id>/
        ├── workspace.json              # Session 元数据和整棵 Panel 树
        └── runs/
            └── <run-id>.json           # 不可变请求与运行结果快照
```

不维护额外的全局 Session 索引。`session.Repository` 启动时扫描受控 Session 子目录，避免跨文件索引和 Workspace 双写。所有目录名来自程序生成的稳定 ID。

## 4. Session 与 Panel 模型

### 4.1 Session

`Session` 包含 ID、用户输入的标题、文件夹字符串、当前 Panel ID、创建和更新时间。文件夹由 Session 字段派生；界面提供筛选和移动，不建立单独文件夹数据库。

新 Session 自动创建一个空根 Panel。标题和文件夹始终由用户输入，不调用 AI。

### 4.2 Panel

`Panel` 包含：

- ID、ParentID 和同级 Order。
- 用户输入的标题与纯文本 Content。
- Included：执行时是否加入完整请求。
- Collapsed：浏览器默认折叠状态。
- KnowledgeIDs 和 AssetIDs。
- Revisions：每次内容性修改前的旧版本。
- 可选 Result 元数据：来源 Run、快捷路径、停止原因和错误摘要。
- 创建和更新时间。

Panel 不保存角色或消息类型。JSON 只是普通 Content；Exa 检测在读取时动态执行。

每次内容修改把旧的标题、正文、Included、知识和 Asset 引用保存为 Revision。折叠和当前选择不产生 Revision。Revision 可预览和恢复，恢复操作本身也产生 Revision。

### 4.3 树规则

- 根 Panel 的 ParentID 为空；其他 Panel 必须引用同一 Session 中存在的父节点。
- 当前工作区只返回并渲染根到当前 Panel 的唯一路径。
- 同级分支按 Order、创建时间和 ID 稳定排序。
- 从任意 Panel 新增输入或执行结果时，创建子 Panel，不覆盖已有子树。
- 同一次完整请求发给多个快捷路径时，每个结果 Panel 的 ParentID 都是执行时的当前 Panel ID，因而结果之间互为兄弟。
- 删除 Panel 会删除其完整子树，并在确认引用同步成功后保存；根 Panel 不能删除。
- “派生新会话”复制根到指定 Panel 的路径、内容和引用，重新生成所有 ID，不复制 Run。

Panel Asset 引用使用 `asset.Reference{Module:"session_panel", RecordID:<panel-id>}`。创建、更新、删除和派生通过 `session.Service` 补偿式同步，防止被引用资产物理删除。

## 5. Provider 配置

### 5.1 主配置迁移

`config.json` Schema 从 1 迁移到 2，并新增 `llm`：

- `providers`：Provider 列表。
- `quick_paths`：工作区快捷发送入口。
- `prompt_templates`：可快速插入 Panel 的文本模板。
- `exa`：API URL、API Key、超时和默认返回内容设置。

配置仍为 `0600`。`config.Repository` 提供并发安全 Snapshot 和 LLM 配置更新；应用无需重启即可读取新配置。API 会返回 Key，因为应用是无鉴权的单用户本机工具，不做伪安全遮罩。默认配置和 Schema 1→2 迁移都创建 ID 固定的本地 llama.cpp Provider 与 `Local` QuickPath；用户删除后仍可用预设 API 重新生成。

### 5.2 Provider

Provider 字段：ID、名称、URL、HTTP 方法、Headers 模板、JSON Body 模板、响应模式、响应提取路径、流式提取路径、可选流式结束路径、连接超时、总超时、最大响应字节、最大 Asset 数据字节和启用状态。

响应模式只有：

- `json`：读取一个有限大小 JSON 响应，通过点分路径提取字符串。
- `sse_json`：解析标准 SSE，忽略注释，把同一事件的多行 `data:` 按规范连接；事件数据必须是 JSON 或 `[DONE]`，通过点分路径提取增量文本，可用布尔结束路径提前结束。

路径只支持 JSON Object 字段和十进制数组索引，例如 `choices.0.text`，不执行表达式。

模板变量：

- `${CONTENT_JSON}`：根到当前节点的已启用 Panel 正文和所选知识拼成的单个 JSON 字符串。
- `${PANELS_JSON}`：Panel 快照数组。
- `${KNOWLEDGE_JSON}`：知识快照数组。
- `${ASSET_DATA_URLS_JSON}`：Asset Data URL 数组。
- `${MODEL_JSON}`：快捷路径模型字符串。
- `${PARAMS_JSON}`：快捷路径参数对象。
- `${API_KEY}`：仅用于 Header 字符串替换。

所有 `_JSON` 变量只允许作为 JSON 值占位符，替换使用 `encoding/json`，最终 Body 必须重新通过 `json.Decoder` 单文档验证且顶层必须是 Object。QuickPath Params 还会浅合并到最终顶层 Object，同名字段覆盖 Provider 模板；因此 llama.cpp 的 `n_predict`、`temperature` 等参数无需写死在模板。Headers 拒绝换行。URL 不执行模板代码。

### 5.3 内置 llama.cpp 预设

llama.cpp Native Completion 预设：

```json
{
  "url": "http://127.0.0.1:8080/completion",
  "method": "POST",
  "body_template": "{\"prompt\":${CONTENT_JSON},\"stream\":true}",
  "response_mode": "sse_json",
  "stream_content_path": "content",
  "stream_done_path": "stop"
}
```

官方文档当前说明 `/completion` 不是 OpenAI 兼容接口，`stream:true` 使用 SSE，增量字符串位于 `content`，并以 `stop` 表示结束。工作台解析注释 ping，不依赖浏览器 EventSource 向 llama.cpp 发 POST。

OpenAI 兼容 completions、chat completions 或 responses 可通过同一通用模板配置；网络层需要角色时，由 Body 模板把 `${CONTENT_JSON}` 包为单个输入，不修改 Panel。

### 5.4 快捷路径与 Prompt 模板

QuickPath 绑定名称、ProviderID、模型和默认参数 JSON。工作区按配置顺序显示文字按钮；可单独点击，也可多选后一次执行。

PromptTemplate 只有 ID、名称和文本。应用模板只把文本插入当前 Panel 光标位置或创建新子 Panel，不隐式执行。

## 6. 完整请求组装与快照

组装器输入当前 Session/Panel、QuickPath、Provider、知识 Repository 和 Asset Repository：

1. 取得根到当前 Panel 路径，只保留 Included Panel。
2. 按路径顺序复制 Panel 内容。
3. 按每个 Panel 的 KnowledgeIDs 顺序复制知识标题、正文、标签和 Asset ID；未知知识条目使执行失败。
4. 按路径顺序收集 Panel AssetIDs 以及所选知识条目的 AssetIDs，去重并读取内容；非图片 Asset 默认拒绝生成 Data URL，避免把视频或任意大附件误送给 LLM。
5. 生成 `CONTENT`：Panel 正文原样连接；Panel 附加知识以标题和正文紧随该 Panel。分隔符固定为两个换行，空内容不增加分隔。
6. 展开 Header/Body 模板并验证大小与 JSON。
7. 在网络调用前写入 Run Snapshot。

Snapshot 保存 Session/Panel/知识/Asset 元数据副本、QuickPath 和 Provider 非密钥配置、最终 URL、脱敏 Headers、完整最终 JSON Body、创建时间。Authorization、API Key 及 `${API_KEY}` 展开结果保存为 `<redacted>`，不复制到 Run 文件。

Asset Data URL 会使 Body 变大，因此每个 Provider 设可配置上限，默认 32 MiB；超过上限在请求前失败。快照仍保存实际发送的完整 Body。

## 7. Run 与流式执行

`provider.Executor` 只负责模板和一次 HTTP 请求。`llm.Manager` 负责 Run 生命周期、持久化、取消、订阅和结果 Panel：

- 状态：`queued`、`running`、`succeeded`、`failed`、`cancelled`、`interrupted`。
- POST 执行先同步写 Snapshot，再返回 `202` 和一个或多个 Run。
- 每个 Run 有独立 Context；用户可以单独取消。
- Manager 将增量追加到有上限的内存文本缓冲并广播 SSE。
- 完成时把完整结果写入 Run，并通过 Session Service 创建结果子 Panel。
- Provider 非 2xx、无效 JSON/SSE、提取路径错误或超限响应进入 failed；有限长度技术内容保存在 Run Error 中。
- 应用重启时，磁盘上的 queued/running Run 标为 interrupted，不自动重发。
- 应用关闭时先拒绝新 Run、取消所有活动请求，再等待 Manager 结束。

浏览器订阅 `GET /api/v1/llm/runs/{id}/events`，先收到 snapshot，再收到 chunk/state；这是工作台自己的 GET SSE，与向 Provider 发出的 POST SSE 分离。

## 8. Exa

Panel Content 只有在整个字符串严格解析为一个 JSON Object、无前后普通文本、无多余 JSON 文档，且满足以下 Schema 时才视为工具候选：

```json
{
  "tool": "exa.search",
  "arguments": {
    "query": "非空字符串",
    "num_results": 8
  }
}
```

- 顶层只允许 `tool` 和 `arguments`。
- `arguments` 只允许 `query` 和可选 `num_results`。
- `num_results` 默认 10，范围 1–100，与 Exa 当前公开 Search API 一致。
- 只显示“执行 Exa”，绝不自动发送。

点击后由 Server 向配置 URL（默认 `https://api.exa.ai/search`）发送 `x-api-key` 和 JSON：`query`、`numResults`、`contents.text=true`。响应受大小和超时限制，原始 JSON 格式化后成为当前 Panel 的子 Panel，并保存请求摘要。非 2xx 错误显示在来源 Panel，不创建伪成功结果。

官方参考：

- llama.cpp Server：`https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md`
- Exa Search：`https://exa.ai/docs/reference/search`

## 9. HTTP API

### 配置

- `GET/PUT /api/v1/llm/config`
- `POST /api/v1/llm/providers/preset/llama-completion`

PUT 提交完整 LLM 配置并严格校验 Provider/QuickPath/Template 引用。配置保存成功后立即影响下一次执行，不改变活动 Run 的 Snapshot。

### Session 与 Panel

- `GET/POST /api/v1/llm/sessions`
- `GET/PUT/DELETE /api/v1/llm/sessions/{session-id}`
- `POST /api/v1/llm/sessions/{session-id}/fork`
- `POST /api/v1/llm/sessions/{session-id}/panels`
- `PUT/DELETE /api/v1/llm/sessions/{session-id}/panels/{panel-id}`
- `POST /api/v1/llm/sessions/{session-id}/panels/{panel-id}/restore/{revision-id}`

GET Session 返回元数据、整棵树、当前路径和分支摘要，避免浏览器自行推导错误树状态。Web DTO 还为每个 Panel 返回 `exa_candidate` 布尔值；该值由 Server 的严格检测器计算，不写入 Workspace，也不由浏览器自行判定。

### 执行

- `POST /api/v1/llm/sessions/{session-id}/execute`
- `POST /api/v1/llm/runs/{run-id}/cancel`
- `GET /api/v1/llm/runs/{run-id}`
- `GET /api/v1/llm/runs/{run-id}/events`
- `POST /api/v1/llm/sessions/{session-id}/panels/{panel-id}/exa`

所有 JSON 写请求严格解码一个对象并限制 Body；错误使用现有 Error Envelope。

## 10. 浏览器界面

LLM 模块使用现有全局左栏：

- 搜索、文件夹筛选和新建会话。
- 会话列表显示用户标题；当前项可修改标题和文件夹。

右侧：

- 顶部为标题、文件夹、派生会话和 Provider 配置入口。
- 中部只显示当前根到节点路径的 Panel 卡片。
- Panel 默认展示标题、启用状态、正文摘要和高频操作；知识、Asset、修订、运行快照和技术错误放进折叠区。
- 有兄弟节点时在对应层显示紧凑文字分支选择器。
- 每个 Panel 可新增子 Panel、执行、恢复修订和删除子树。
- 底部固定执行栏显示 QuickPath 文字按钮、多选发送和活动 Run 状态。

配置模块新增 Provider、QuickPath、PromptTemplate 和 Exa 折叠编辑区。高级 Headers、Body Template 和提取路径使用 JSON/文本编辑器，不在默认视图铺开。

前端新增 `static/llm.js`，避免继续扩张现有 `app.js`。它只通过传入的 DOM、API helper 和全局 Asset Picker 与 Shell 协作。

窄屏延续现有左栏抽屉；Panel 单列、执行栏换行，Dialog 接近全屏。拖拽不作为唯一排序方式。

## 11. 测试与验收

仅用 Go 标准库：

- Repository：树约束、路径、兄弟顺序、修订、删除子树、派生会话、重开持久化和深拷贝。
- Service：Panel Asset 引用创建/替换/删除/派生及失败补偿。
- Config：Schema 1→2 迁移、Provider 引用校验、运行时保存和 `0600`。
- Assembler：Panel/知识顺序、模板 JSON 安全、Header 换行拒绝、Data URL、大小上限和快照脱敏。
- Executor：真实 `httptest.Server` 同步 JSON、llama.cpp SSE、OpenAI 风格自定义路径、ping、`[DONE]`、取消、超时和错误截断。
- Manager：多 QuickPath 兄弟结果、SSE replay、取消、重启 interrupted 和关闭等待。
- Exa：严格单 JSON 检测、参数范围、用户触发、官方字段映射和错误不建 Panel。
- Web/App：API CRUD、严格错误、装配、响应式页面契约和实际生命周期。

验收时使用可控 `httptest.Server` 模拟 Provider 和 Exa，不访问真实外部服务，不需要任何 API Key。
