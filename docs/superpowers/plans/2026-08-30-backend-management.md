# 后端管理实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有单体程序中交付可运行时编辑的后端配置、Linux Shell 进程组启停、原始实时日志、手动保存与异常退出日志。

**Architecture:** `internal/backend` 独立拥有 Profile Store、命令模板、内存日志和进程 Manager；`internal/web` 只通过接口暴露 JSON API 与 SSE。应用启动时构造一个 Manager，HTTP Server 与优雅关闭共同持有它。

**Tech Stack:** Go 1.24 标准库、Linux `/bin/bash`、`syscall`、SSE、原生 JavaScript

**Spec:** `docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md`

## Global Constraints

- 同一后端配置最多运行一个实例，不检测不同配置之间的 GPU、显存或端口冲突。
- 原始命令通过 `/bin/bash -lc` 在独立 Linux 进程组中执行。
- 停止整个进程组：先 `SIGTERM`，超时后 `SIGKILL`。
- stdout/stderr 合并为原始文本，不附加时间戳、来源标签或 JSONL。
- 日志正常情况下只存内存；用户手动保存或进程异常退出时才落普通文本。
- 前后端继续保持零第三方依赖，只监听 `127.0.0.1`。

---

### Task 1: 后端 Profile、命令模板与持久化

**Files:**
- Create: `internal/backend/profile.go`
- Create: `internal/backend/profile_test.go`
- Create: `internal/backend/repository.go`
- Create: `internal/backend/repository_test.go`
- Create: `internal/backend/template.go`
- Create: `internal/backend/template_test.go`

**Interfaces:**
- Produces: `backend.Profile`, `backend.Readiness`, `backend.Profile.Validate()`
- Produces: `backend.ExpandCommand(command string, values map[string]string) (string, error)`
- Produces: `backend.OpenRepository(path string) (*Repository, error)`
- Produces: `List`, `Get`, `Create`, `Update`, `Delete` Profile 方法

- [ ] **Step 1: 写 Profile 与模板失败测试**

Profile 必须包含 ID、名称、说明、标签、命令、工作目录、环境变量、默认变量、就绪规则、停止宽限秒数和日志字节上限。校验要求名称与命令非空、宽限 `1..300` 秒、日志容量 `64 KiB..64 MiB`，就绪类型只允许 `none`、`delay`、`http`、`log_regex`。

模板测试验证 `${MODEL}` 原样替换、`${MODEL_SH}` 使用 POSIX 单引号安全引用、未知变量返回错误、`$$` 保留一个美元符号。

- [ ] **Step 2: 运行测试并确认因接口缺失而失败**

Run: `go test ./internal/backend -run 'Test(Profile|Expand)' -v`

- [ ] **Step 3: 实现 Profile 校验和受控模板展开**

模板只解析 `${[A-Z0-9_]+}`；`_SH` 从同名基础变量取值并用 POSIX 单引号编码。配置变量与启动覆盖合并后再展开，不执行嵌套替换。

- [ ] **Step 4: 写 Repository 失败测试**

测试空目录首次打开、创建后立即持久化、名称/ID 查找、更新保留 ID、删除、重复 ID 拒绝、重新打开恢复数据和并发读取。文档格式为：

```go
type profileDocument struct {
    SchemaVersion int       `json:"schema_version"`
    Profiles      []Profile `json:"profiles"`
}
```

- [ ] **Step 5: 实现带互斥锁的 Repository**

ID 使用 `crypto/rand` 生成 16 字节十六进制；列表按名称稳定排序；每次变更通过 `store.WriteJSON(..., 0o600)` 原子保存。

- [ ] **Step 6: 运行并提交 Task 1**

Run: `go test ./internal/backend -v`

```bash
git add internal/backend
git commit -m "feat: add backend profile storage"
```

### Task 2: 有界原始日志与订阅

**Files:**
- Create: `internal/backend/log_buffer.go`
- Create: `internal/backend/log_buffer_test.go`

**Interfaces:**
- Produces: `backend.NewLogBuffer(capacity int) *LogBuffer`
- Produces: `Write`, `Snapshot`, `Clear`, `Subscribe` 和取消订阅方法

- [ ] **Step 1: 写日志 Buffer 失败测试**

测试原始字节保持不变、超限只保留最新字节、Clear 只清当前缓冲区、订阅者收到后续 Chunk、慢订阅者不会阻塞进程输出、取消后 Channel 关闭。

- [ ] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/backend -run TestLogBuffer -v`

- [ ] **Step 3: 实现并发安全环形缓冲与广播**

使用 `sync.Mutex` 保护字节切片和订阅者表。写入时复制 Chunk；订阅 Channel 有固定缓冲，满时丢弃该订阅者的旧 Chunk并发送最新 Chunk，绝不阻塞 `Write`。

- [ ] **Step 4: 运行并提交 Task 2**

Run: `go test ./internal/backend -v`

```bash
git add internal/backend/log_buffer.go internal/backend/log_buffer_test.go
git commit -m "feat: add transient backend logs"
```

### Task 3: Linux 进程 Manager

**Files:**
- Create: `internal/backend/manager_linux.go`
- Create: `internal/backend/manager_linux_test.go`

**Interfaces:**
- Produces: `backend.NewManager(repository, crashLogDir) *Manager`
- Produces: `Start(profileID string, overrides map[string]string) (RunInfo, error)`
- Produces: `Stop(ctx context.Context, profileID string) error`
- Produces: `Runs() []RunInfo`, `Run(profileID string) (RunInfo, bool)`
- Produces: `LogSnapshot`, `SubscribeLog`, `ClearLog`, `SaveLog`, `Shutdown`

- [ ] **Step 1: 写启动、单实例和原始输出失败测试**

使用短 Shell Fixture 验证工作目录、环境变量、模板变量、stdout/stderr 合并进入 Buffer、启动时保存 Profile 快照、同 Profile 第二次启动被拒绝。

- [ ] **Step 2: 运行目标测试并确认失败**

Run: `go test ./internal/backend -run 'TestManager(Start|Rejects)' -v`

- [ ] **Step 3: 实现启动与 Wait 状态机**

状态为 `starting`、`running`、`stopping`、`stopped`、`failed`。`exec.Cmd` 使用 `SysProcAttr.Setpgid=true`，stdout/stderr 指向同一个 `LogBuffer`。`Wait` 后记录退出码；非用户停止且退出码非零时，将 Buffer 写到 crash log。

- [ ] **Step 4: 写停止整个进程组失败测试**

Fixture 启动父 Shell 和后台子进程并写出子 PID。调用 Stop 后轮询两个 PID，确认进程组被终止；另一个 Fixture 忽略 TERM，验证宽限超时后 KILL。测试手动保存日志只写当前快照，正常停止不自动产生日志文件。

- [ ] **Step 5: 实现停止、保存和 Shutdown**

Stop 对进程组发送负 PID 信号，等待完成 Channel，超时后 KILL。Shutdown 对全部活动 Profile 并行调用 Stop 并汇总错误。保存路径由 Manager 分配，名称只使用运行 ID 和安全固定后缀。

- [ ] **Step 6: 写并实现就绪检测**

测试并实现四种模式：`none` 立即运行、`delay` 延时后运行、`http` 轮询 2xx、`log_regex` 在原始日志匹配后运行。进程提前退出时不得进入 running；规则超时转为 failed 并停止进程。

- [ ] **Step 7: 运行并提交 Task 3**

Run: `go test ./internal/backend -count=1 -v`

```bash
git add internal/backend/manager_linux.go internal/backend/manager_linux_test.go
git commit -m "feat: manage backend process groups"
```

### Task 4: 后端 JSON API 与 SSE

**Files:**
- Create: `internal/web/backend.go`
- Create: `internal/web/backend_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Produces: `/api/v1/backends` Profile CRUD
- Produces: `/api/v1/backends/{id}/start`, `/stop`, `/runs`, `/logs`, `/logs/events`, `/logs/save`, `/logs/clear`

- [ ] **Step 1: 写 CRUD、生命周期和校验失败测试**

使用真实临时 Repository 与 Manager，通过 `httptest` 验证创建、列出、修改、删除、启动、重复启动冲突、停止、未知 ID 和无效 JSON 的状态码及 Error Envelope。

- [ ] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/web -run TestBackend -v`

- [ ] **Step 3: 实现后端 Handler**

所有写请求使用 `http.MaxBytesReader` 和严格单 JSON 解码。路径 ID 通过 Go 1.24 ServeMux 通配符读取。创建返回 `201`，启动返回 `202`，冲突返回 `409`，无效输入返回 `400`。

- [ ] **Step 4: 写并实现 SSE 测试**

连接时先发送当前 Snapshot，再转发 Chunk；事件名为 `snapshot` 与 `chunk`，Data 是 JSON 字符串。Context 取消时解除订阅。响应设置 `text/event-stream`、`no-cache` 和 `X-Accel-Buffering: no`。

- [ ] **Step 5: 将 Repository 与 Manager 装配进 App**

App 启动时打开 `<data-dir>/backends/profiles.json`，Manager 使用 `<data-dir>/backends/crash-logs`；关闭 HTTP 接收后调用 Manager.Shutdown，再释放实例锁。

- [ ] **Step 6: 运行并提交 Task 4**

Run: `go test ./internal/web ./internal/app -count=1 -v`

```bash
git add internal/web internal/app
git commit -m "feat: expose backend management API"
```

### Task 5: 后端管理浏览器界面

**Files:**
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/server_test.go`

**Interfaces:**
- Consumes: Task 4 的 Backends API 与 SSE
- Produces: 紧凑 Profile 列表、编辑抽屉、启停操作、状态和原始日志查看器

- [ ] **Step 1: 写嵌入页面契约失败测试**

验证后端模块包含 Profile 列表挂载点、编辑表单、命令文本区、启动/停止、日志 `<pre>`、保存和清屏操作，并且 JavaScript 请求 Backends API 与 `EventSource`。

- [ ] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/web -run TestEmbeddedBackend -v`

- [ ] **Step 3: 实现简洁后端管理 UI**

默认只显示名称、状态、运行时长和启停按钮。新增/编辑在抽屉中展示命令、目录、环境变量、默认变量、就绪和日志容量；高级字段使用 `<details>`。日志区保持 `textContent` 原样追加，提供自动滚动暂停、搜索、清屏和保存。

- [ ] **Step 4: 运行并提交 Task 5**

Run: `go test ./internal/web -v`

```bash
git add internal/web/static internal/web/server_test.go
git commit -m "feat: add backend management workspace"
```

### Task 6: 阶段验证、文档与推送

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-08-30-backend-management.md`

- [ ] **Step 1: README 增加 Profile、命令信任边界和日志说明**

- [ ] **Step 2: 执行完整验证**

Run: `gofmt -w cmd internal && go vet ./... && go test ./... -count=1 && git diff --check`

Run: `go build ./cmd/ai-workbench`

Expected: 全部退出码为 0。

- [ ] **Step 3: 勾选已验证计划项并提交**

```bash
git add README.md docs/superpowers/plans/2026-08-30-backend-management.md
git commit -m "docs: record backend management delivery"
```

- [ ] **Step 4: 推送并核对远端**

Run: `git push origin main`

Expected: `git rev-parse HEAD` 与 `git rev-parse origin/main` 相同，工作树干净。
