# 远端进程 Worker 设计

**日期：** 2026-08-31

**状态：** 已交付

## 1. 目标与边界

本阶段增加一个独立的 Linux 命令 `ai-worker`，让本地工作台通过 SSH 隧道在云端启动、观察和停止 `llama-server`、`sd-server` 等进程。Worker 是最小化的进程控制面，不是第二个工作台，也不是生成任务代理。

本阶段包含：

- 只监听云端 `127.0.0.1` 的 HTTP API。
- 任一时刻最多管理一个 Linux 进程组。
- 接收完整 Shell 命令、工作目录、环境变量和运行参数快照。
- 启动、查询、停止、实时日志和日志快照。
- 工作台后端 Profile 可选择本地执行或指定 Worker 执行。
- Worker 地址与模型 Server Provider 地址完全分开配置。

本阶段不包含：

- 文件上传、下载、同步、共享目录管理或 Asset API。
- Prompt、模型请求、生成结果、工作台配置或后端 Profile 持久化。
- Worker 上的生图/视频 CLI 任务。
- 鉴权、TLS、反向代理、服务发现、集群调度、GPU/显存检测或端口冲突检测。
- 公网监听选项、任意监听地址参数或浏览器直接访问 Worker。

Worker API 能执行任意用户命令，因此安全边界固定为 Linux 用户权限、回环地址和 SSH 隧道；不得把端口直接暴露到公网。

## 2. 连接拓扑

控制面和数据面使用不同端口：

```text
浏览器
  |
  v
本地 ai-workbench
  |-- SSH tunnel A --> 远端 127.0.0.1:<worker-port> --> ai-worker
  |                                                  `--> llama-server / sd-server 进程组
  |
  `-- SSH tunnel B --> 远端 127.0.0.1:<model-port>  --> llama-server / sd-server HTTP API
```

工作台只向 Worker 发送进程控制数据。LLM、生图和视频的 HTTP 请求仍由各自 Provider 直接访问映射后的模型 Server 端口。由此保证 Worker 不接触 Panel、Prompt、API Key、Asset 和生成结果。

SSH 隧道由用户建立和维护；工作台不执行 `ssh`、不保存 SSH 凭据，也不尝试自动修复隧道。

## 3. 交付物与程序生命周期

仓库新增 `cmd/ai-worker`，与 `cmd/ai-workbench` 使用同一 Go Module 和相同的“仅标准库”约束。它们构建为两个相互独立的二进制；工作台自身仍是单体 Web 应用。

Worker 参数：

- `--port`：可选，默认 `8288`，只用于 `127.0.0.1:<port>`。
- `--version`：输出版本并退出。

Worker 不需要数据目录和配置文件。进程状态、命令快照和日志都只存在于内存。收到 `SIGINT` 或 `SIGTERM` 时，Worker 停止接收请求，按当前运行的宽限设置终止整个进程组，然后关闭 HTTP Server。

Worker 使用 Linux 进程组启动 `/bin/bash -lc <command>`。启动 Shell 设置父进程死亡信号；正常退出和可捕获信号下由 Worker 终止进程组。`SIGKILL`、主机掉电等不可恢复场景仍可能留下孙进程，这一限制在运行说明中明确，不伪装成强保证。

## 4. Worker 运行模型

Worker 全局只有一个 Slot，而不是按 Profile 各自一个 Slot。状态为：

- `idle`：没有活动进程；可以保留最近一次终态摘要。
- `starting`：进程已启动，正在执行就绪检测。
- `running`：未配置就绪检测，或检测已通过。
- `stopping`：已经开始终止进程组。
- `stopped`：用户请求停止或进程正常退出。
- `failed`：启动、就绪或进程运行失败。

`start` 在 `starting`、`running` 或 `stopping` 时返回冲突，不排队、不自动停止旧进程。每次成功启动生成随机 `run_id`，后续停止和日志请求都携带该 ID；不匹配的旧页面操作返回冲突，避免误停后来启动的任务。

运行快照包含：

- `run_id`、状态、PID、开始/结束时间、退出码和有限错误文本。
- 完整命令、工作目录、环境变量、停止宽限、日志容量和就绪规则。
- Worker 实例启动时生成的 `instance_id`。

Worker 重启后状态回到 `idle`，不根据 PID 猜测或接管旧进程。

## 5. HTTP API

所有接口位于 `/api/v1/`，只接受严格 JSON，拒绝未知字段。请求正文、字符串长度、环境变量数量和日志响应都有固定上限。响应沿用工作台的稳定错误 Envelope。

### 5.1 健康与状态

`GET /api/v1/health`

返回版本、`instance_id` 和 `status: ok`，不包含主机环境、文件列表或其他敏感信息。

`GET /api/v1/process`

返回当前运行快照；空闲且没有历史时返回 `run: null`。工作台通过该接口在页面重连后恢复状态。

### 5.2 启动

`POST /api/v1/process/start`

```json
{
  "command": "./llama-server --model ./model.gguf --port 8080",
  "work_dir": "/srv/models",
  "env": {"CUDA_VISIBLE_DEVICES": "0"},
  "stop_grace_seconds": 10,
  "log_buffer_bytes": 1048576,
  "readiness": {
    "kind": "http",
    "url": "http://127.0.0.1:8080/health",
    "timeout_seconds": 120
  }
}
```

Worker 不做模板展开；模板变量只在持有 Profile 的工作台中展开，Worker 收到的是最终命令。工作目录必须是绝对路径；环境变量覆盖 Worker 继承环境中的同名值。

就绪规则与现有本地后端一致：`none`、`delay`、`http`、`log_regex`。HTTP 就绪 URL 只允许 `http://127.0.0.1`、`http://localhost` 或 IPv6 回环地址，不允许 Worker 被用作通用网络探测器。

### 5.3 停止

`POST /api/v1/process/{run_id}/stop`

停止时向整个进程组发送 `SIGTERM`，等待运行快照中的宽限时间，再发送 `SIGKILL` 并等待回收。相同 `run_id` 的重复停止是幂等的；不同 ID 返回 `409 run_mismatch`。

### 5.4 日志

- `GET /api/v1/process/{run_id}/logs`：返回当前原始日志快照，Content-Type 为纯文本。
- `GET /api/v1/process/{run_id}/logs/events`：先发送当前快照，再通过 SSE 发送新的原始文本块。

stdout 和 stderr 合并进入有容量上限的内存环形缓冲。Worker 不提供清空日志和保存日志接口，也不写 crash log。工作台的“清空”只清除浏览器视图；“保存”读取当前远端快照并写入工作台数据目录。这样远端 Worker 不产生持久化业务数据。

断开 SSE 不影响进程。日志中间发生截断时，状态响应提供 `log_start_offset` 和 `log_end_offset`；客户端发现偏移不连续时重新读取快照，而不是重复拼接错误内容。

## 6. 工作台集成

后端 Profile Schema 升级并新增执行位置：

```json
{
  "execution": {
    "kind": "local"
  }
}
```

或：

```json
{
  "execution": {
    "kind": "worker",
    "worker_base_url": "http://127.0.0.1:8288"
  }
}
```

旧 Profile 迁移为 `local`。命令、变量、工作目录、环境、就绪规则、宽限和日志容量继续由 Profile 保存；启动时先由工作台完成变量展开，再把不可变快照发送给 Worker。

`backend.Manager` 通过内部 `WorkerClient` 接口和 Profile 的执行位置选择本地或 Worker 路径。Web API 和界面继续按 Profile ID 工作，不暴露两套操作模型。远端 Run 在本地记录 `worker_instance_id` 与 `worker_run_id`，防止 Worker 重启后把旧状态错配到新进程。

模型 Provider URL 不从 Profile 推导。用户分别配置：

- Worker 隧道地址，例如 `http://127.0.0.1:8288`。
- 模型 Server 隧道地址，例如 `http://127.0.0.1:8080`。

Worker 不可达时，工作台显示“控制连接断开”，不把远端进程判定为已停止。恢复连接后用 `instance_id` 和 `run_id` 对账；实例已变化则标记原 Run 为未知/中断，不对新 Slot 发出停止请求。

## 7. 界面

后端 Profile 编辑器增加一个默认折叠的“执行位置”：

- 本机：保持现有行为。
- 远端 Worker：显示 Worker URL 和“测试连接”。

列表仍只突出名称、运行状态、启停按钮和短日志。执行位置、Worker 实例/Run ID、完整命令快照与连接错误放在 `<details>` 中。远端日志复用现有实时日志面板；保存后的文件位于工作台本机。

## 8. 失败与恢复语义

- 启动请求只有在子进程成功获得 PID 后才返回成功；命令启动失败返回终态错误。
- 工作台在启动响应丢失时先查询 Worker 状态，不盲目重发。只有传输层结果不明且完整请求快照语义相等时才接管查询到的 Run；Worker 已明确返回 `slot_busy` 等 HTTP 错误时绝不接管现有 Run。
- 隧道中断不停止进程；状态标记为连接未知。
- Worker 有序关闭时停止受管进程组。
- Worker 意外退出后不承诺接管遗留 PID；重新启动后 Slot 为空。
- 工作台关闭时对已连接的远端 Run 发一次有界停止请求。失败会报告，但不会无限阻塞退出。
- Worker 不保存日志；若云主机或 Worker 崩溃，未手动保存的日志可能丢失，这是刻意的数据隔离取舍。

## 9. 验证

自动化测试只使用 Go 标准库：

- Linux Shell Fixture 验证单 Slot、进程组 TERM/KILL、退出码和 Worker 关闭。
- `httptest` 验证严格 JSON、Run ID 冲突、请求上限、状态、日志快照和 SSE。
- 临时脚本验证 stdout/stderr 原文、环形截断和日志偏移重连。
- 假 Worker 验证工作台 Profile 的本地/远端路由、实例换代和网络错误。
- 迁移测试验证既有 Profile 自动成为 `local`，并且原字段无损。
- 实际二进制烟测验证 Worker 只绑定 `127.0.0.1`。

## 10. 验收标准

用户可以在云端运行一个无配置、无数据目录的 `ai-worker`，通过 SSH 映射它的回环端口，在本地工作台中选择远端执行并启动一个 Server。工作台能实时显示原始日志、查询状态和停止进程；实际 LLM/图像/视频请求通过另一个端口直达模型 Server。Worker 上不会留下 Profile、Prompt、Asset、结果或自动日志文件。

## 11. 交付记录

实现位于 `cmd/ai-worker`、`internal/worker`、`internal/backend` 和现有后端管理页面。后端 Profile 文件已从 Schema v1 原子迁移到 v2，旧 Profile 明确设为 `execution.kind: local`。远端日志通过绝对偏移衔接快照与 SSE Chunk；发生截断或重连时重新对齐，不重复或静默遗漏已知区间。

交付验证覆盖严格 JSON、请求/响应上限、单 Slot、进程组 TERM/KILL、四类就绪检测、重定向拒绝、Run ID 冲突、原始日志/SSE、Profile 迁移、远端启停、连接中断、实例换代、启动响应丢失、手动本机保存、应用关闭和固定回环监听。`localhost` 就绪探测在实际拨号前解析全部地址并拒绝任何非回环结果；工作台日志 SSE 以 Base64 和权威字节偏移传输原始数据，因此环形缓冲从半个 UTF-8 字符开始时也不会破坏清屏与重连位置。关闭流程为启动恢复、远端停止和最终日志对账保留了明确的有界清理时间。Worker 与工作台均可独立构建，未增加 Go Module 或前端依赖。
