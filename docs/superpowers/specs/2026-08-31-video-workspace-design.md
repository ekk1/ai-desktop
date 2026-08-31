# 视频工作区设计

**日期：** 2026-08-31

**状态：** 已批准，待实施

## 1. 目标与范围

本阶段交付独立的视频生成工作区。用户维护带文件夹的视频批次，为多条 Prompt 配置公共视频参数和单项覆盖，通过 stable-diffusion.cpp 官方异步 HTTP API 或本机自定义 CLI 批量运行，把结果导入共享 Asset 库，并可按需提取尾帧继续生成。

本阶段包含：

- 视频专用 Provider、Batch、Item、Attempt 和执行快照。
- stable-diffusion.cpp 原生 `/sdcpp/v1/vid_gen` 适配器。
- 首帧、尾帧和有序控制帧 Asset 输入。
- 时长、FPS、请求帧数与实际帧数记录。
- 本机 Shell CLI 预设、固定任务工作区和用户选择素材暂存。
- 可配置的外部尾帧提取命令。
- 视频和尾帧结果导入 Asset 库，默认进入 `archive`。
- 有限并发、实时状态、取消、重试和重启中断恢复。

本阶段不包含：

- 与生图共用参数对象、表单、批次或 Attempt。
- 浏览器视频编辑器、时间线、转码器、播放器库或内置 `ffmpeg`。
- 自动 Prompt 扩写、自动精选、自动串联下一条视频或自动保存日志。
- 远端 Worker 上的 CLI 素材传输和结果回传。
- 在首版修改或 Fork stable-diffusion.cpp。

## 2. stable-diffusion.cpp 能力结论

截至 2026-08-31，官方异步 Server 已提供 `POST /sdcpp/v1/vid_gen`、Job 查询和取消接口。HTTP 视频请求支持 T2V、首帧、尾帧、有序控制帧、Prompt、Negative Prompt、尺寸、Seed、FPS、帧数、常规与 High-noise Sample Params、LoRA、VAE Tiling、Cache 和 VACE Strength；响应提供编码后的视频容器、MIME、FPS 和实际帧数。官方输出格式为 WebM、Animated WebP 和 MJPG AVI，其中 WebM/WebP 取决于编译选项。[Server API](https://github.com/leejet/stable-diffusion.cpp/blob/master/examples/server/api.md) [Server 路由能力](https://github.com/leejet/stable-diffusion.cpp/blob/master/examples/server/routes_sdcpp.cpp) [构建说明](https://github.com/leejet/stable-diffusion.cpp/blob/master/docs/build.md)

当前 HTTP 缺口同样明确：`vid_gen` 不接受 `ref_images`、`ref_video`、参考音频、配对音轨、Mask、单张 Control Image 或 IP Adapter Image；单个 Job 只返回一条视频；生成开始后 Server 不支持真正取消，只能取消仍在队列中的 Job；Capabilities 是模式级而非模型级，不能证明当前模型支持某个高级条件。请求帧数还可能被模型规则归一化，因此必须保存响应中的实际帧数。[Server API](https://github.com/leejet/stable-diffusion.cpp/blob/master/examples/server/api.md) [运行时实现](https://github.com/leejet/stable-diffusion.cpp/blob/master/examples/server/runtime.cpp)

CLI 的模型专用能力更丰富。例如 Wan 文档使用 `--control-video` 消费帧目录；MiniMax-H3 支持可重复参考图片、参考视频帧目录、参考音频和首尾帧；LTX 系列支持 T2V、I2V 和首尾帧视频。`--ref-video` 这类输入因此先走用户配置的本机 CLI，而不伪造为 HTTP 字段。[Wan 指南](https://github.com/leejet/stable-diffusion.cpp/blob/master/docs/wan.md) [MiniMax-H3 指南](https://github.com/leejet/stable-diffusion.cpp/blob/master/docs/minimax_h3.md) [LTX 指南](https://github.com/leejet/stable-diffusion.cpp/blob/master/docs/ltx2.md)

stable-diffusion.cpp 官方说明其 CLI/API 仍可能频繁变化，所以工作台保留原生 JSON 覆盖与清晰的适配器边界，不把当前字段固化进跨模块模型。[项目 README](https://github.com/leejet/stable-diffusion.cpp/blob/master/README.md)

结论：现有 Server 足以交付常规 HTTP 视频基线，首版不 Fork。高级 Ref2Video/音频走本机 CLI。若以后必须在云端通过 HTTP 使用这些能力，优先扩展 stable-diffusion.cpp Server 的 JSON/路由/媒体限制并新增适配器版本；底层 CLI 已有部分推理能力，通常不需要从核心推理重写。

## 3. 领域隔离

新增 `internal/videogen`，它不复用 `imagegen.Batch`、`imagegen.Item` 或图像参数。共享内容仅限：

- 通过 `internal/asset` 读取输入和导入结果。
- HTTP Job 传输可复用 `internal/sdcpp` 的有界请求、同源轮询 URL 和错误处理原语。
- CLI 进程可复用后端管理已经验证的 Linux 进程组终止模式，但不作为 Backend Profile 运行。
- 页面复用现有 Shell、Asset Picker、错误 Envelope 和 SSE 约定。

视频批次和图像批次分别持久化、配置和展示，未来协议变化不会迫使图像字段一起迁移。

## 4. 配置模型

主配置 Schema 升级，新增互相独立的三类预设。

### 4.1 HTTP Provider

一个 Video HTTP Provider 包含：

- ID、名称、启用状态和 stable-diffusion.cpp Base URL。
- 自定义请求头，允许配置 Key。
- 请求超时、轮询间隔、最大并发 Job 数。
- 最大请求正文、最大远端错误正文、最大视频响应和最大输入图片总量。
- 默认视频参数 JSON。

Provider 默认请求官方 `/sdcpp/v1/vid_gen`。Capabilities 只作为模式可用性和输出格式提示，不根据通用 Feature 列表隐藏模型相关字段。

### 4.2 CLI Preset

一个本机 CLI 预设包含：

- ID、名称、启用状态、原始 Shell 命令模板。
- 可选素材准备命令模板。
- 工作目录、环境变量、超时、停止宽限和内存日志容量。
- 期望输出的精确相对路径和 MIME/扩展名。
- 默认视频参数 JSON。

CLI 预设由受信任用户配置，通过 `/bin/bash -lc` 执行。首版只允许 `execution_kind: local_cli`；不把命令交给远端 Worker，因为 Worker 不传输素材和结果。

### 4.3 Tail Frame Preset

尾帧预设包含名称、原始 Shell 命令模板、超时、停止宽限、输出扩展名和最大图片大小。推荐由用户配置 `ffmpeg`，但工作台不探测、不下载也不捆绑它。

## 5. Batch、Item 与 Attempt

### 5.1 Batch

Batch 包含 ID、文件夹、标题、执行预设类型与 ID、公共参数、默认并发、Item 顺序、创建和更新时间。标题和文件夹完全由用户输入。

删除 Batch 前先移除它对输入/输出 Asset 的引用；Asset 本体仍由 Gallery 生命周期管理。

### 5.2 Item

Item 包含：

- ID、Prompt、Negative Prompt、启用状态和顺序。
- 单项参数覆盖 JSON。
- 可选 Init Image、End Image。
- 有序 Control Frame Asset ID 数组。
- CLI 专用的有序素材选择，每项带角色标签，如 `reference_image`、`reference_video`、`reference_audio` 或用户自定义角色。
- Attempt 历史和当前结果摘要。

Item 只允许选择 active Asset，但一旦引用成功，即使 Asset 后来归档也继续保留，直到用户明确移除。

### 5.3 参数与时长

公共参数和单项覆盖使用递归 Object 合并，单项值覆盖公共值，`null` 显式删除继承字段。常用字段由表单编辑，完整对象在高级 JSON 区可见。

工作台提供两种互斥输入：

- 直接填写 `video_frames`。
- 填写 `duration_seconds` 和 `fps`，按明确的取整规则计算请求帧数。

执行快照同时保存用户输入时长、FPS、计算后的请求帧数和算法版本。工作台不擅自按某个模型修正为 `4n+1`；Server 如有归一化，以 Job 结果 `frame_count` 保存实际值并在界面并列显示。

### 5.4 Attempt

每次运行或重试都创建不可变执行快照。Attempt 包含：

- ID、Batch/Item ID、执行种类、预设快照和合并后的参数。
- 输入 Asset 的 ID、哈希、MIME、大小、用途和顺序。
- 请求时长、FPS、请求帧数、实际帧数。
- HTTP Remote Job ID/状态/队列位置，或 CLI PID/工作区/有限日志。
- 状态、错误、创建/开始/结束时间和输出 Asset ID。

状态统一为 `queued`、`submitting`、`polling`、`running`、`succeeded`、`failed`、`cancelled` 和 `interrupted`。HTTP 通常经历 submitting/polling，CLI 经历 queued/running；前端不据此猜测协议细节。

## 6. HTTP 执行

HTTP 组装顺序：

1. 校验 Provider 和 Batch/Item。
2. 合并 Provider 默认、Batch 公共和 Item 覆盖。
3. 由常用字段写入 Prompt、Negative Prompt、FPS 和最终请求帧数。
4. 读取 Init、End、Control Frame 图片 Asset，验证类型与大小，并编码为 Data URL。
5. 只允许工作台管理的字段覆盖输入媒体，防止高级 JSON 偷换为任意本地路径。
6. 在任何外部请求前持久化 Attempt 快照。
7. POST `/sdcpp/v1/vid_gen`，随后只轮询同一 Provider Base URL 下的相对 Job 地址。
8. 成功后有界解码单个视频结果，验证 MIME 与魔数，再导入 Asset。

HTTP Item 每次提交一个 Job。批量抽卡由工作台为多 Item 或多次重复创建独立 Attempt，不依赖 Server 的 `batch_count`。

取消 queued Attempt 只更新本地状态。已得到 Remote Job ID 时发送官方 cancel 请求；若 Server 表明生成已开始且不可取消，Attempt 保持活动并显示“远端生成中，当前 Server 不支持中途取消”，继续轮询直至终态，避免谎报已取消却留下未知计算。

## 7. CLI 执行与固定工作区

每个 CLI Attempt 使用：

```text
<data-dir>/video-workspace/<attempt-id>/
├── manifest.json
├── inputs/
└── outputs/
```

只暂存用户明确选择的 Asset。优先创建指向 Asset 受控文件的硬链接；跨文件系统或不支持时复制。文件名包含稳定序号和净化后的扩展名，例如 `inputs/003-reference-video.webm`。`manifest.json` 保存 Asset ID、角色、顺序、原内容哈希、暂存相对路径和方式。

准备命令可把视频拆成帧目录，或完成模型专用预处理。主命令只有在准备命令成功后执行。工作台提供稳定模板变量的 Shell 引用形式：

- Attempt/工作区：`ATTEMPT_ID`、`WORKSPACE_DIR`、`INPUT_DIR`、`OUTPUT_DIR`、`OUTPUT_PATH`。
- 内容：`PROMPT`、`NEGATIVE_PROMPT`、`SEED`、`FPS`、`VIDEO_FRAMES`、`DURATION_SECONDS`。
- 输入：`INIT_IMAGE`、`END_IMAGE`、`CONTROL_FRAMES_JSON`、`SELECTED_ASSETS_JSON`、`MANIFEST_PATH`。

所有普通变量默认替换为 Shell 安全单参数；显式 `_RAW` 变量只用于受信任的复合片段，并在界面警示。用户负责把清单角色转换为 `--ref-video` 等模型参数。

预设声明唯一精确 `OUTPUT_PATH`。命令成功后只读取该路径，不递归扫描目录、不采用“最新文件”。文件必须位于 Attempt 的 `outputs/` 内，通过大小、扩展名/MIME 和基本魔数验证后导入 Asset。

取消 CLI 时终止整个进程组。CLI 日志只进入 Attempt 的有界内存流；用户可手动保存到工作台数据目录，不自动保存。工作台重启时遗留运行状态标为 `interrupted`，不重新执行命令。

成功/失败 Attempt 的工作区默认保留，便于复现和排查；界面提供单项“清理工作区”，且必须先核对路径严格位于 `video-workspace/<attempt-id>`。清理不会删除 Asset 库文件。

## 8. 尾帧提取

用户从成功的视频 Asset 或 Attempt 结果点击“提取尾帧”，选择 Tail Frame Preset。工作台创建独立提取记录和临时输出路径，提供 `INPUT_VIDEO`、`OUTPUT_IMAGE`、`ASSET_ID` 的安全模板变量，运行外部命令并显示有限日志。

只有命令成功、输出存在、文件非空、大小合规且识别为允许的图片类型时才导入 Asset。尾帧默认是 `archive`，可以立即“设为精选”；精选后可作为后续视频首帧或生图/LLM 图片输入。重复提取允许生成多个 Asset 记录，物理内容仍按 SHA-256 去重。

提取失败不改变原视频 Asset。提取记录在重启后按与 CLI Attempt 相同规则标为 `interrupted`。

## 9. 持久化与资产引用

```text
<data-dir>/videos/batches/<batch-id>/batch.json
<data-dir>/video-workspace/<attempt-id>/...
```

Batch 文件使用带版本号的原子 JSON。配置中的 Provider/CLI/Tail Preset 由主配置迁移。Attempt 在外部 HTTP 或进程启动前落盘，结果逐个导入，因此尾帧失败不影响视频结果，后续保存失败也不会覆盖既有历史。

引用来源至少区分：Item 输入、Attempt 输入快照、Attempt 视频结果和尾帧结果。归档不解除引用；物理删除继续由 Asset 服务执行引用保护。

## 10. 调度、恢复与关闭

- 每个预设拥有共享并发上限，Batch 可设置更低的局部上限。
- 同一 Item 不同时运行两个活动 Attempt；重复抽卡通过复制 Item 或完成后再次重试产生新历史。
- 浏览器关闭或 SSE 断开不取消任务。
- 工作台启动时把遗留活动 Attempt/提取记录标为 `interrupted`，不猜测远端 Job 或本地 PID。
- 正常关闭停止接收新任务，取消本机 CLI 进程组；对 HTTP queued Job 尝试取消，对已经生成且 Server 拒绝取消的 Job 只做有界处理后退出。
- 部分成功导入的结果不回滚。

## 11. API 与界面

API 分为：视频配置、文件夹/Batch CRUD、Item CRUD/排序、运行/重试/取消、Attempt SSE、工作区清理、尾帧提取。执行 API 返回创建的 Attempt；Batch GET 返回完整领域状态和相关 Asset 摘要。

页面顶部“视频”保持桌面左右布局：

- 左栏：搜索、文件夹、Batch、新建和选中状态。
- 右侧顶部：标题、文件夹、执行预设、保存与运行。
- 中部：高频公共参数和批量 Prompt Item。
- 底部：响应式结果卡片、原生 `<video controls preload="metadata">` 和尾帧操作。

窄屏左栏进入抽屉，主区单列。默认只展示 Prompt 摘要、状态、视频预览和高频操作。完整参数、时长换算、执行快照、远端 Job ID、CLI 路径/日志、模型限制和历史 Attempt 使用 `<details>`；必要错误必须直接可见，不只放 tooltip。

## 12. 安全与资源上限

- Provider 只能来自已保存配置；轮询 URL 必须是同一 Base URL 下的相对官方路径。
- 输入只从 Asset 受控目录读取；输出和清理只允许 Attempt 固定工作区内路径。
- 外部 HTTP 正文、Data URL 输入总量、视频输出、图片尾帧和日志都有硬上限。
- HTTP Client 不自动跟随跨源重定向。
- CLI/提取命令是受信任本机用户能力，不提供给浏览器访客之外的安全隔离；应用仍只允许 SSH 隧道访问。
- 远端 Worker 不用于视频 CLI，避免命令接口隐式获得文件读取/回传能力。

## 13. 验证

自动化测试只使用 Go 标准库：

- 参数递归合并、`null` 删除、时长换算和不可变快照。
- HTTP Body 组装、输入媒体顺序、同源轮询、响应上限、实际帧数和不可取消语义。
- Batch/Item/Attempt 持久化、Asset 引用、重试、中断恢复和部分成功。
- 临时目录与 Shell Fixture 验证素材暂存、Manifest、精确输出路径、进程组取消和路径逃逸拒绝。
- 尾帧 Fixture 验证成功导入、无输出、超限、错误类型和取消。
- `httptest` 验证所有 JSON API、错误 Envelope 与 SSE 生命周期。
- 实际二进制烟测和桌面/窄屏手工验收，不引入前端测试依赖。

## 14. 验收标准

用户可以创建独立的视频 Batch，批量配置 Prompt、时长/FPS/帧数及首尾/控制素材，通过 stable-diffusion.cpp HTTP 生成常规视频；也可以用本机自定义 CLI 模板和固定工作区执行 `ref_video` 等高级流程。成功视频自动归档到 Asset 库，用户可手动提取尾帧、精选并继续下一次生成。所有高级参数与日志保持折叠，工作台不需要 Fork stable-diffusion.cpp，也不会把素材交给远端 Worker。
