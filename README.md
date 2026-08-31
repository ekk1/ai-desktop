# AI Desktop

一个面向 Linux 本地 AI 工作流的单体 Web 工作台。后端仅使用 Go 标准库，前端仅使用原生 HTML、CSS 和 JavaScript，最终构建为一个内嵌页面资源的二进制。

当前已完成基础骨架、后端管理、共享资产、知识备忘录、LLM 完整请求工作区和生图工作区：

- 固定监听 `127.0.0.1` 的 HTTP Server
- 可覆盖的数据目录和监听端口
- 同一数据目录的 Linux 排他实例锁
- 带版本号、备份和原子替换的 JSON 配置存储
- `/api/v1/health` 与 `/api/v1/settings`
- 桌面双栏、窄屏抽屉式的响应式工作台 Shell
- LLM、生图、视频、后端管理、Gallery、知识库和配置入口
- 运行时新增、编辑、复制基础信息和删除后端 Profile
- `/bin/bash -lc` 原始命令、工作目录、环境变量和模板变量
- 同一 Profile 单实例、Linux 进程组启停和可选就绪检测
- 不落盘的原始实时日志、手动保存和异常退出 crash log
- Gallery 多文件导入、状态/文本筛选、媒体预览、备注和受控下载
- active 精选库与 archive 归档库、批量状态调整和多选 ZIP 导出
- 可供后续模块复用、只展示 active 内容的 Asset Picker
- 带文件夹、标签、正文和 Asset 引用的知识备忘录
- 资产引用保护：知识条目仍在引用的文件不能物理删除
- 无聊天角色语义的会话文件夹、Panel 树、分支选择和修订恢复
- 从任意 Panel 新建子分支或派生独立会话
- 可运行时维护的 Provider、QuickPath、Prompt Template 和 Exa 配置
- llama.cpp `/completion` 本地预设与通用 JSON/SSE HTTP 请求
- 多 QuickPath 并行执行、不可变快照、SSE 增量、取消和结果 Panel
- 严格识别、必须由用户确认执行的 Exa JSON 请求
- stable-diffusion.cpp 原生异步 Image Provider、capabilities 和运行时配置
- 带文件夹的生图批次、完整参数 JSON、批量 Prompt 和单项覆盖
- 有限并发、不可变 Attempt 快照、SSE 状态、取消、重试与中断恢复
- 五类图片 Asset 输入、结果自动归档和一键精选

下一阶段先交付可选的[远端进程 Worker](docs/superpowers/specs/2026-08-31-remote-worker-design.md)，再按独立的[视频工作区设计](docs/superpowers/specs/2026-08-31-video-workspace-design.md)接入视频生成。Worker 只经 SSH 隧道控制一个云端进程组，不传输 Prompt、Asset 或结果；生成请求通过另一条隧道直达模型 Server。已交付的 [LLM](docs/superpowers/specs/2026-08-30-llm-workspace-design.md) 与[生图](docs/superpowers/specs/2026-08-30-image-workspace-design.md)行为分别记录在独立设计中。

## 要求

- Linux
- Go 1.24 或更高版本

程序没有第三方 Go Module、前端包、CDN 或构建工具依赖。

## 构建

```bash
go build -o ai-workbench ./cmd/ai-workbench
```

可通过链接参数写入版本号：

```bash
go build -ldflags '-X main.version=0.1.0' -o ai-workbench ./cmd/ai-workbench
./ai-workbench --version
```

## 运行

```bash
./ai-workbench --data-dir ./workbench-data --port 8188
```

参数：

- `--data-dir`：数据目录；省略时使用 `$XDG_DATA_HOME/ai-workbench`，未设置 XDG 变量时使用 `$HOME/.local/share/ai-workbench`。
- `--port`：运行时端口覆盖；省略时读取配置，首个版本默认为 `8188`。
- `--version`：输出版本并退出。

浏览器访问 `http://127.0.0.1:8188/`。

工作台只监听本机回环地址，并且不提供鉴权、用户隔离或浏览器侧秘密保护。能够访问页面的人可以查看和修改配置中的 API Key，也能执行配置的本地命令。仅通过本机或受信任的 SSH 隧道访问，不要用反向代理直接暴露到公网。

## SSH 隧道

在浏览器所在电脑运行：

```bash
ssh -L 8188:127.0.0.1:8188 user@linux-host
```

保持 SSH 会话连接，然后访问 `http://127.0.0.1:8188/`。

## 验证

```bash
go vet ./...
go test ./... -count=1
go build ./cmd/ai-workbench
```

健康检查：

```bash
curl http://127.0.0.1:8188/api/v1/health
```

响应示例：

```json
{"status":"ok","version":"dev"}
```

## 后端管理

打开顶部“后端管理”，点击“新建配置”，填写原始 Shell 命令后即可启动。配置保存在：

```text
<data-dir>/backends/profiles.json
```

每个 Profile 最多运行一个实例；不同 Profile 可以并行运行。工作台不检测端口、GPU 或显存冲突。

命令由 `/bin/bash -lc` 执行，因此配置内容等同于当前 Linux 用户的本地命令执行权限，只应录入自己信任的命令。工作台停止进程时会操作整个独立进程组，而不是只停止顶层 Shell。

模板变量有两种形式：

- `${MODEL}`：原样替换，适合由配置者明确控制的命令片段。
- `${MODEL_SH}`：读取 `MODEL` 的值并进行 POSIX Shell 单引号编码，路径和普通文本优先使用这种形式。

如果命令需要保留 Shell 自己的 `${HOME}` 展开，写成 `$${HOME}`；工作台展开后交给 Bash 的内容就是 `${HOME}`。

就绪检测支持立即就绪、固定等待、HTTP 2xx 和日志正则。日志只保留在配置容量限制的内存缓冲区中：正常停止后不会自动写文件；点击“保存”时写入 `manual-*.log`，异常退出时自动写入 `crash-*.log`。

## LLM 完整请求工作区

打开顶部“LLM 工作区”，在左栏新建会话并自行填写标题、文件夹。会话只是 Panel 树的组织容器，不具有 user、assistant 或 system 对话角色。右侧只显示服务端选定的根到当前节点路径：每个 Panel 可显式保存内容、加入或排除完整请求、折叠、恢复修订、新建子 Panel，或从该节点派生一个只复制当前路径的新会话。分支选择器用于切换同一父节点下的不同后续。

Panel 可从知识库和 Asset Picker 选择引用。Picker 只展示 `active` 精选资产；已经引用后再归档的 Asset 会保留在 Panel 中，直到点击对应“移除”并保存。当前 LLM 组装器只把图片 Asset 编码为 Data URL，普通附件和视频不能加入 LLM 请求。Prompt Template 的“插入”只把纯文本写入当前草稿，不会自动保存或执行。

会话和每次运行保存在：

```text
<data-dir>/sessions/<session-id>/workspace.json
<data-dir>/sessions/<session-id>/runs/<run-id>.json
```

点击一个 QuickPath 的“发送”，或勾选多个 QuickPath 后批量发送，会先持久化各自的不可变请求快照，再并行请求 Provider。增量内容通过工作台 SSE 显示；成功结果在来源 Panel 下创建互为兄弟的普通 Panel。取消或正常关闭会取消活动请求。程序重新启动时，磁盘上遗留的 `queued` 或 `running` Run 会标为 `interrupted`，不会自动重发。

### Provider、QuickPath 与模板

打开顶部“配置”维护 LLM 配置。配置和 API Key 位于 `<data-dir>/config.json`，程序创建时权限为 `0600`；修改保存后影响下一次 Run，不改变已经生成的快照。“添加 llama.cpp 预设”会添加默认 Provider `llama-local` 和 QuickPath `local`，目标为 `POST http://127.0.0.1:8080/completion`，Body 为 `{"prompt":${CONTENT_JSON},"stream":true}`，SSE 增量路径为 `content`，结束路径为 `stop`。如果同 ID 已存在，预设不会覆盖它。

Provider Body Template 必须是单个 JSON Object，变量必须作为完整 JSON 值使用：

- `${CONTENT_JSON}`：已加入请求的 Panel 内容和所选知识拼成的字符串。
- `${PANELS_JSON}`：已加入请求的 Panel 快照数组。
- `${KNOWLEDGE_JSON}`：所选知识快照数组。
- `${ASSET_DATA_URLS_JSON}`：所选图片的 Data URL 数组。
- `${MODEL_JSON}`：QuickPath 的模型名。
- `${PARAMS_JSON}`：QuickPath 参数 Object。

QuickPath 的 Params 还会在渲染后合并到请求体顶层；同名字段以 Params 为准。Header 只支持 `${API_KEY}` 变量。密钥和敏感 Header 在 Run 快照中会被清空或替换为 `<redacted>`。

响应模式为 `json` 时，用点分路径从单个 JSON 响应提取字符串，例如 `choices.0.text`。模式为 `sse_json` 时，每条 SSE `data` 必须是 JSON 或 `[DONE]`；“增量路径”提取文本，“结束路径”可指向布尔值。路径只支持 Object 字段和十进制数组索引，不是 JSONPath，也不会执行表达式。连接超时、总超时、响应上限和图片总字节上限都在 Provider 中配置。

### 手动 Exa

在“配置”中填写 Exa API Key。只有当一个 Panel 的全部正文严格等于下列单个 JSON Object，且字段和范围通过服务端校验时，界面才显示“执行 Exa”：

```json
{
  "tool": "exa.search",
  "arguments": {
    "query": "检索内容",
    "num_results": 8
  }
}
```

浏览器不会自行解析后自动联网。必须点击按钮并再次确认，工作台 Server 才会发送请求；Exa 原始 JSON 结果会成为来源 Panel 的子 Panel，可继续交给任意 QuickPath 分析。`num_results` 可省略，允许范围为 1–100。

## 生图工作区

工作台使用 stable-diffusion.cpp 官方原生异步接口：`/sdcpp/v1/capabilities`、`/sdcpp/v1/img_gen` 和 `/sdcpp/v1/jobs/*`。默认 Provider 为 `sdcpp-local`，Base URL 是 `http://127.0.0.1:1234`。可在顶部“配置”中修改 URL、固定 Headers、超时、轮询间隔、响应/图片字节上限、并发数和启用状态；这些设置位于 `<data-dir>/config.json`。

Provider 只定义 HTTP 请求路径，不启动模型 Server。请在“后端管理”中自行配置并启停 `sd-server` 命令、模型和启动参数；切换模型时先停止旧 Profile，再启动目标 Profile。生图界面的“读取 capabilities”只读取当前模型并补充 sampler、scheduler 和输出格式建议，不会覆盖未保存参数，Server 不可达也不影响编辑批次。

打开顶部“生图”后，可按文件夹管理 Batch。每个 Batch 显式选择 Provider、局部并发数和公共 Base Params；批量添加时每个非空行成为一个有序 Item。Item 可单独设置 Prompt、Negative Prompt、完整 Params Override，或复制、上移、下移、删除、运行、取消和重试。批次并发和 Provider 全局并发会同时生效，较小者限制当前 Batch，Provider 限制还会跨 Batch 共享。

常用字段和“完整 Base Params JSON”编辑的是同一个 JSON Object。公共参数与单项覆盖按 Object 递归合并，单项的 scalar、array 或 `null` 替换公共值；未知 stable-diffusion.cpp 字段会原样保留。以下字段由工作台根据 Item 和 Asset 引用强制生成，不能写入 Base Params 或 Params Override：

```text
prompt, negative_prompt, init_image, ref_images,
mask_image, control_image, ip_adapter_image
```

Item 可从 active 精选库选择 init、refs、mask、control 和 IP Adapter 图片；选择器只显示 `image/*`。已引用后归档的输入仍保留，直到在 Item 中明确移除。请求发送前，工作台读取受控 Asset 文件并在内存中生成 Data URL；持久化快照只保存 ID、哈希、类型、尺寸和大小，不保存 Base64。

成功图片会逐张导入共享 Asset 库并默认进入 `archive`，同时显示在当前 Batch 结果区；点击“设为精选”后才会出现在其他模块的 active Picker。部分图片导入成功、后续图片失败时，已导入结果仍保留，Attempt 记录失败原因。

批次数据位于：

```text
<data-dir>/images/batches/<batch-id>/batch.json
```

每次运行和重试都会新增 Attempt；其中执行快照不可变，状态和远端进度会持续更新，旧历史不会被重试覆盖。状态包括 `queued`、`submitting`、`polling`、`succeeded`、`failed`、`cancelled` 和 `interrupted`，浏览器通过工作台 SSE 实时更新。工作台重启后，遗留的活动 Attempt 会标为 `interrupted`，不会猜测远端状态或自动重发；需要时手动重试即可。

## 共享资产与 Gallery

打开顶部“Gallery”后，可一次导入多个图片、视频或普通附件。新导入内容默认进入 `archive`；只有手动设为 `active` 的精选内容会出现在知识库以及后续生成模块的 Asset Picker 中。

资产索引和受控内容分别保存在：

```text
<data-dir>/assets/index.json
<data-dir>/assets/files/
```

物理文件按 SHA-256 内容哈希命名，同内容只保存一份文件，但每次导入仍保留独立的 Asset 记录。Gallery 支持搜索、预览、备注、单文件下载、批量精选/归档以及多选 ZIP 导出。

Asset 被知识条目或其他模块引用时仍可归档，但不能物理删除。预览面板会展示阻止删除的引用来源。

## 知识备忘录

打开顶部“知识库”后，可自行维护文件夹、标题、标签、纯文本正文和关联 Asset。这里不执行 embedding、RAG、自动召回或 AI 标题生成；后续 LLM 工作区只会按用户选择快速引用这些内容。

知识条目保存在：

```text
<data-dir>/knowledge/notes.json
```

新建、更新或删除知识条目时，工作台会同步维护 Asset 引用。选择新 Asset 时只显示 active 精选库；已经关联、之后又被归档的 Asset 会继续保留，直到用户在条目中明确移除。
