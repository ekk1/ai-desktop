# Remote Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付一个只监听 Linux 回环地址、一次管理一个进程组的独立 `ai-worker`，并让工作台后端 Profile 可以在本机或该 Worker 上执行。

**Architecture:** `internal/worker` 拥有无持久化的单 Slot 进程服务、严格 HTTP Handler 和有界客户端；`cmd/ai-worker` 只负责信号与 HTTP 生命周期。`internal/backend` 保留 Profile 和浏览器 API 的统一语义，以 `execution.kind` 选择现有本机运行或远端 Worker 运行；Worker 地址只走控制面，模型 Provider 地址仍单独直连。

**Tech Stack:** Go 1.24+ 标准库、Linux process groups、`syscall`、`net/http`、JSON、SSE、原生 HTML/CSS/JavaScript。

**Spec:** `docs/superpowers/specs/2026-08-31-remote-worker-design.md`

## Global Constraints

- 工作台和 Worker 只使用 Go 标准库；前端不增加第三方包、CDN 或构建步骤。
- Worker 固定绑定 `127.0.0.1`，不提供监听公网地址的参数。
- Worker 不创建数据目录，不持久化命令、Profile、日志、Prompt、Asset 或结果。
- Worker 全局一次最多运行一个 Linux 进程组；不排队，也不自动替换活动进程。
- Worker 不提供文件传输、生成代理、SSH 管理、GPU 检测、端口冲突检测、鉴权或 TLS。
- 进程通过 `/bin/bash -lc` 启动；停止时对进程组先 `SIGTERM`，宽限到期后 `SIGKILL`。
- Worker URL 与模型 Server Provider URL 永远分开配置。
- 所有网络正文、字符串、环境变量、错误文本和日志缓冲都有明确上限。
- 每个实现任务使用测试先行；测试必须先观察到预期失败，再写最小实现。

## 文件结构

- Create `internal/worker/protocol.go`：Worker API 的请求、状态、运行快照、校验和错误码。
- Create `internal/worker/manager_linux.go`：单 Slot Linux 进程组、就绪检测、原始内存日志和关闭。
- Create `internal/worker/handler.go`：严格 `/api/v1/` HTTP、纯文本日志和 SSE。
- Create `internal/worker/client.go`：工作台使用的有界 Worker HTTP/SSE 客户端。
- Create `internal/worker/*_test.go`：协议、进程、Handler、Client 和生命周期测试。
- Create `cmd/ai-worker/main.go`：参数、信号、固定回环监听和版本输出。
- Create `cmd/ai-worker/main_test.go`：参数和真实监听地址烟测所需的可测入口。
- Modify `internal/backend/profile.go`：增加 `Execution` 值对象和校验。
- Modify `internal/backend/repository.go`：Profile Schema v1→v2 迁移。
- Create `internal/backend/remote_run.go`：Worker Run 映射、状态对账和日志代理。
- Modify `internal/backend/manager_linux.go`：按执行位置启动本机或远端 Run。
- Modify `internal/backend/*_test.go`：迁移、路由、断线、实例换代、保存日志和关闭测试。
- Modify `internal/web/backend.go`、`internal/web/backend_test.go`：Context 启动、Worker 连接测试和稳定错误映射。
- Modify `internal/web/static/index.html`：折叠的执行位置字段和远端状态详情。
- Modify `internal/web/static/app.js`：Profile 读写、测试连接、远端标签与浏览器清屏。
- Modify `internal/web/static/styles.css`：复用现有响应式表单样式，补充执行位置行。
- Modify `internal/app/app.go`、`internal/app/app_test.go`：注入 Worker Client、关闭远端 Run。
- Modify `README.md`、总体设计、规格和本计划：运行方式、SSH 双隧道、验收记录与阶段状态。

---

### Task 1: Worker 协议与严格校验

**Files:**
- Create: `internal/worker/protocol.go`
- Test: `internal/worker/protocol_test.go`

**Interfaces:**
- Produces: `StartRequest.Validate() error`、`Readiness.Validate() error`、`Run`、`RunState`、`StatusResponse`、`APIError` 和协议上限常量。
- Consumes: 仅 Go 标准库。

- [ ] **Step 1: 写失败的协议校验测试**

```go
func TestStartRequestValidate(t *testing.T) {
	t.Parallel()
	valid := worker.StartRequest{
		Command: "./llama-server --port 8080", WorkDir: "/srv/models",
		Env: map[string]string{"CUDA_VISIBLE_DEVICES": "0"},
		StopGraceSeconds: 10, LogBufferBytes: 1 << 20,
		Readiness: worker.Readiness{Kind: worker.ReadinessHTTP, URL: "http://127.0.0.1:8080/health", TimeoutSeconds: 60},
	}
	if err := valid.Validate(); err != nil { t.Fatalf("Validate() error = %v", err) }

	cases := map[string]worker.StartRequest{
		"empty command":      withCommand(valid, ""),
		"relative work dir":  withWorkDir(valid, "models"),
		"public readiness":   withReadinessURL(valid, "http://10.0.0.8:8080/health"),
		"bad env":            withEnv(valid, map[string]string{"BAD=KEY": "x"}),
		"small log buffer":   withLogBytes(valid, 1024),
		"long stop grace":    withGrace(valid, 301),
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(); err == nil { t.Fatal("Validate() error = nil") }
		})
	}
}
```

- [ ] **Step 2: 运行测试并确认因协议类型不存在而失败**

Run: `go test ./internal/worker -run 'TestStartRequestValidate'`

Expected: FAIL，错误包含 `undefined: worker.StartRequest`。

- [ ] **Step 3: 实现完整协议类型和边界**

```go
const (
	MaxRequestBytes = int64(1 << 20)
	MaxCommandBytes = 256 << 10
	MaxWorkDirBytes = 16 << 10
	MaxEnvEntries = 256
	MinLogBufferBytes = 64 << 10
	MaxLogBufferBytes = 64 << 20
)

type Readiness struct {
	Kind string `json:"kind"`
	DelaySeconds int `json:"delay_seconds,omitempty"`
	URL string `json:"url,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	TimeoutSeconds int `json:"timeout_seconds"`
}

type StartRequest struct {
	Command string `json:"command"`
	WorkDir string `json:"work_dir,omitempty"`
	Env map[string]string `json:"env,omitempty"`
	StopGraceSeconds int `json:"stop_grace_seconds"`
	LogBufferBytes int `json:"log_buffer_bytes"`
	Readiness Readiness `json:"readiness"`
}

type RunState string

const (
	StateStarting RunState = "starting"
	StateRunning RunState = "running"
	StateStopping RunState = "stopping"
	StateStopped RunState = "stopped"
	StateFailed RunState = "failed"
)

type Run struct {
	RunID string `json:"run_id"`
	InstanceID string `json:"instance_id"`
	State RunState `json:"state"`
	PID int `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	EndedAt *time.Time `json:"ended_at,omitempty"`
	ExitCode *int `json:"exit_code,omitempty"`
	Error string `json:"error,omitempty"`
	LogStartOffset int64 `json:"log_start_offset"`
	LogEndOffset int64 `json:"log_end_offset"`
	Request StartRequest `json:"request"`
}

type StatusResponse struct { Run *Run `json:"run"` }
```

`Validate` 必须拒绝空命令、非绝对非空工作目录、非法环境变量名、超过上限的字段、宽限范围外数值、日志容量范围外数值、无效正则，以及非 HTTP/非回环的 HTTP 就绪 URL。允许 `localhost`、`127.0.0.0/8` 和 `::1`；解析主机名后不得访问其他地址。

- [ ] **Step 4: 运行协议测试和格式检查**

Run: `gofmt -w internal/worker/protocol.go internal/worker/protocol_test.go && go test ./internal/worker`

Expected: PASS。

- [ ] **Step 5: 提交协议边界**

```bash
git add internal/worker/protocol.go internal/worker/protocol_test.go
git commit -m "feat: define remote worker protocol"
```

### Task 2: 单 Slot Linux 进程服务

**Files:**
- Create: `internal/worker/log_buffer.go`
- Create: `internal/worker/manager_linux.go`
- Test: `internal/worker/log_buffer_test.go`
- Test: `internal/worker/manager_linux_test.go`

**Interfaces:**
- Consumes: Task 1 的 `StartRequest` 和 `Run`。
- Produces: `NewManager(instanceID string) *Manager`、`Start(context.Context, StartRequest) (Run, error)`、`Stop(context.Context, string) (Run, error)`、`Status() StatusResponse`、`LogSnapshot(string) (LogSnapshot, error)`、`SubscribeLog(string) (LogSnapshot, <-chan LogChunk, func(), error)`、`Shutdown(context.Context) error`。

- [ ] **Step 1: 写单 Slot、日志和进程组失败测试**

```go
func TestManagerRunsOnlyOneProcessGroup(t *testing.T) {
	manager := worker.NewManager("instance-test")
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	first, err := manager.Start(context.Background(), shellRequest("trap 'exit 0' TERM; echo ready; while :; do sleep 1; done"))
	if err != nil { t.Fatal(err) }
	if first.State != worker.StateRunning { t.Fatalf("state = %q", first.State) }
	if _, err := manager.Start(context.Background(), shellRequest("echo second")); !errors.Is(err, worker.ErrSlotBusy) {
		t.Fatalf("second Start() error = %v", err)
	}
	if _, err := manager.Stop(context.Background(), "stale-run"); !errors.Is(err, worker.ErrRunMismatch) {
		t.Fatalf("stale Stop() error = %v", err)
	}
	stopped, err := manager.Stop(context.Background(), first.RunID)
	if err != nil { t.Fatal(err) }
	if stopped.State != worker.StateStopped { t.Fatalf("state = %q", stopped.State) }
}
```

另写测试覆盖 stdout/stderr 原文合并、环形截断偏移、订阅取消、HTTP/日志正则/延迟就绪、就绪失败终止、自然失败退出码、TERM 超时后的整个进程组 KILL，以及 `Shutdown` 回收活动进程。

- [ ] **Step 2: 运行测试并确认缺少 Manager**

Run: `go test ./internal/worker -run 'TestManager|TestLogBuffer'`

Expected: FAIL，错误包含 `undefined: worker.NewManager`。

- [ ] **Step 3: 实现带绝对偏移的原始日志环形缓冲**

```go
type LogSnapshot struct {
	StartOffset int64
	EndOffset int64
	Data []byte
}

type LogChunk struct {
	Offset int64
	Data []byte
}

func (buffer *LogBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	start := buffer.endOffset
	buffer.endOffset += int64(len(data))
	buffer.bytes = append(buffer.bytes, data...)
	if extra := len(buffer.bytes) - buffer.capacity; extra > 0 {
		buffer.bytes = append([]byte(nil), buffer.bytes[extra:]...)
		buffer.startOffset += int64(extra)
	}
	buffer.publish(LogChunk{Offset: start, Data: append([]byte(nil), data...)})
	return len(data), nil
}
```

慢订阅者采用容量固定的 Channel；满时关闭该订阅，客户端会根据偏移重新拉取快照，不能阻塞模型进程输出。

- [ ] **Step 4: 实现 Linux Manager 和就绪状态机**

```go
command := exec.CommandContext(context.Background(), "/bin/bash", "-lc", request.Command)
command.Dir = request.WorkDir
command.Env = append(os.Environ(), sortedEnvironment(request.Env)...)
command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
command.Stdout = logBuffer
command.Stderr = logBuffer
```

Manager 必须先在锁内验证 Slot 空闲并保留启动状态，再启动命令；生成 128-bit 随机 `run_id`。`Stop` 对 `-PID` 发信号，Context 提前到期时也先 `SIGKILL` 再等待最多一秒。自然退出只在非用户停止且退出码非零或就绪失败时进入 `failed`。状态读取返回深拷贝，不暴露内部 Map/Slice。

- [ ] **Step 5: 运行进程测试、Race 和 Vet**

Run: `gofmt -w internal/worker && go test -race ./internal/worker && go vet ./internal/worker`

Expected: 全部 PASS。

- [ ] **Step 6: 提交进程服务**

```bash
git add internal/worker/log_buffer.go internal/worker/log_buffer_test.go internal/worker/manager_linux.go internal/worker/manager_linux_test.go
git commit -m "feat: manage one remote worker process group"
```

### Task 3: Worker HTTP Handler 与有界客户端

**Files:**
- Create: `internal/worker/handler.go`
- Create: `internal/worker/client.go`
- Test: `internal/worker/handler_test.go`
- Test: `internal/worker/client_test.go`

**Interfaces:**
- Consumes: Task 2 的 Manager API。
- Produces: `NewHandler(version string, manager *Manager) http.Handler`；`Client{BaseURL, HTTPClient, MaxResponseBytes}` 及 `Health`、`Status`、`Start`、`Stop`、`Logs`、`SubscribeLogs`。

- [ ] **Step 1: 写严格路由和客户端失败测试**

```go
func TestHandlerRejectsUnknownStartFields(t *testing.T) {
	handler := worker.NewHandler("test", worker.NewManager("instance-test"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/process/start", strings.NewReader(`{
		"command":"echo ok","stop_grace_seconds":10,"log_buffer_bytes":65536,
		"readiness":{"kind":"none","timeout_seconds":60},"unknown":true
	}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest { t.Fatalf("status = %d", response.Code) }
	if !strings.Contains(response.Body.String(), `"code":"invalid_json"`) { t.Fatalf("body = %s", response.Body.String()) }
}
```

Client 测试用 `httptest.Server` 覆盖 Base URL 正规化、非 2xx Error Envelope、有界响应、超时、纯文本日志、SSE snapshot/chunk/offset、畸形 SSE 和调用者取消。

- [ ] **Step 2: 运行测试并确认 Handler/Client 不存在**

Run: `go test ./internal/worker -run 'TestHandler|TestClient'`

Expected: FAIL，错误包含 `undefined: worker.NewHandler` 或 `undefined: worker.Client`。

- [ ] **Step 3: 实现固定路由和错误 Envelope**

```go
mux.HandleFunc("GET /api/v1/health", handler.health)
mux.HandleFunc("GET /api/v1/process", handler.status)
mux.HandleFunc("POST /api/v1/process/start", handler.start)
mux.HandleFunc("POST /api/v1/process/{run_id}/stop", handler.stop)
mux.HandleFunc("GET /api/v1/process/{run_id}/logs", handler.logs)
mux.HandleFunc("GET /api/v1/process/{run_id}/logs/events", handler.logEvents)
```

`start` 使用 `http.MaxBytesReader`、`json.Decoder.DisallowUnknownFields()` 并确认只有一个 JSON 值。`logs` 返回 `text/plain; charset=utf-8`，并用 `X-Log-Start-Offset`/`X-Log-End-Offset` 传递偏移。SSE 的 `snapshot` 与 `chunk` Data 是 JSON：

```json
{"offset":0,"data":"raw server output\n"}
```

Slot 忙和 Run 不匹配返回 409；不存在历史返回 404；校验错误返回 400；内部错误返回不泄露堆栈的 500。

- [ ] **Step 4: 实现有界客户端和 SSE 解析**

```go
type Client struct {
	BaseURL string
	HTTPClient *http.Client
	MaxResponseBytes int64
}

func (client Client) Start(ctx context.Context, request StartRequest) (Run, error)
func (client Client) Stop(ctx context.Context, runID string) (Run, error)
func (client Client) Status(ctx context.Context) (StatusResponse, error)
func (client Client) Logs(ctx context.Context, runID string) (LogSnapshot, error)
func (client Client) SubscribeLogs(ctx context.Context, runID string) (<-chan LogEvent, <-chan error, error)
```

客户端拒绝 Base URL 中的 UserInfo、Query 和 Fragment，不跟随重定向，响应读取使用 `io.LimitReader(limit+1)`。SSE Scanner 显式提高到协议允许的单事件上限，并在事件 Data 超限或偏移非法时终止。

- [ ] **Step 5: 运行 Worker 包完整验证**

Run: `gofmt -w internal/worker && go test -race ./internal/worker && go vet ./internal/worker`

Expected: 全部 PASS。

- [ ] **Step 6: 提交 HTTP 协议实现**

```bash
git add internal/worker/handler.go internal/worker/handler_test.go internal/worker/client.go internal/worker/client_test.go
git commit -m "feat: expose remote worker control api"
```

### Task 4: 独立 ai-worker 命令与有序关闭

**Files:**
- Create: `cmd/ai-worker/main.go`
- Create: `cmd/ai-worker/main_test.go`
- Create: `internal/worker/server.go`
- Test: `internal/worker/server_test.go`

**Interfaces:**
- Consumes: Task 3 的 `NewHandler` 和 Task 2 的 `Manager.Shutdown`。
- Produces: `worker.RunServer(ctx context.Context, options ServerOptions) error`；可构建的 `./cmd/ai-worker`。

- [ ] **Step 1: 写监听和关闭失败测试**

```go
func TestRunServerBindsLoopbackAndStopsManagedProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	addresses := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- worker.RunServer(ctx, worker.ServerOptions{
			Port: 0, Version: "test", OnListen: func(address string) { addresses <- address },
		})
	}()
	address := <-addresses
	host, _, err := net.SplitHostPort(address)
	if err != nil { t.Fatal(err) }
	if host != "127.0.0.1" { t.Fatalf("host = %q", host) }
	cancel()
	if err := <-done; err != nil { t.Fatalf("RunServer() error = %v", err) }
}
```

另测非法端口、`--version` 输出、未知参数非零退出，以及活动 Shell 在 Context 取消后退出。

- [ ] **Step 2: 运行测试并确认 Server 入口不存在**

Run: `go test ./internal/worker ./cmd/ai-worker`

Expected: FAIL，错误包含 `undefined: worker.RunServer`。

- [ ] **Step 3: 实现固定回环 Server 生命周期**

```go
listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(options.Port)))
server := &http.Server{
	Handler: NewHandler(options.Version, manager),
	ReadHeaderTimeout: 5 * time.Second,
	IdleTimeout: 60 * time.Second,
}
```

Context 结束后并行调用 `server.Shutdown` 和 `manager.Shutdown`，使用 15 秒有界 Context，等待 `Serve` 返回并用 `errors.Join` 汇总错误。`instance_id` 在每次 Worker 启动时随机生成。

- [ ] **Step 4: 实现命令参数和信号入口**

```go
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ai-worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := flags.Int("port", 8288, "loopback HTTP port")
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); err != nil { return 2 }
	if *showVersion { fmt.Fprintln(stdout, version); return 0 }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := worker.RunServer(ctx, worker.ServerOptions{Port: *port, Version: version}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
```

- [ ] **Step 5: 构建、测试并检查只绑定回环**

Run: `gofmt -w cmd/ai-worker internal/worker && go test -race ./cmd/ai-worker ./internal/worker && go build -o /tmp/ai-worker-check ./cmd/ai-worker && /tmp/ai-worker-check --version`

Expected: 测试 PASS、构建成功、版本输出非空。

- [ ] **Step 6: 提交 Worker 二进制**

```bash
git add cmd/ai-worker internal/worker/server.go internal/worker/server_test.go
git commit -m "feat: add standalone remote worker command"
```

### Task 5: Backend Profile 执行位置与 Schema 迁移

**Files:**
- Modify: `internal/backend/profile.go`
- Modify: `internal/backend/profile_test.go`
- Modify: `internal/backend/repository.go`
- Modify: `internal/backend/repository_test.go`

**Interfaces:**
- Consumes: Task 3 客户端接受的 Worker Base URL 规则。
- Produces: `Execution{Kind, WorkerBaseURL}`、`ExecutionLocal`、`ExecutionWorker`；Profile Schema Version 2。

- [ ] **Step 1: 写默认值、校验和 v1 迁移失败测试**

```go
func TestOpenRepositoryMigratesVersionOneExecutionToLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	writeLegacyProfiles(t, path, `{"schema_version":1,"profiles":[{
		"id":"local-one","name":"Local","command":"echo ok",
		"readiness":{"kind":"none","timeout_seconds":60},
		"stop_grace_seconds":10,"log_buffer_bytes":1048576
	}]}`)
	repository, err := backend.OpenRepository(path)
	if err != nil { t.Fatal(err) }
	profile, _ := repository.Get("local-one")
	if profile.Execution.Kind != backend.ExecutionLocal { t.Fatalf("kind = %q", profile.Execution.Kind) }
	var saved map[string]any
	readJSON(t, path, &saved)
	if saved["schema_version"] != float64(2) { t.Fatalf("schema = %#v", saved["schema_version"]) }
}
```

另测 Worker URL 必须为绝对 HTTP(S)、无 UserInfo/Query/Fragment、Host 非空；`local` 禁止携带 URL，`worker` 必须携带 URL。为了兼容手写请求，空 `execution.kind` 在 `DefaultProfile` 和迁移中成为 `local`，但 Update 后持久化显式值。

- [ ] **Step 2: 运行测试并确认 Execution 字段不存在**

Run: `go test ./internal/backend -run 'Test.*Execution|TestOpenRepositoryMigrates'`

Expected: FAIL，错误包含 `profile.Execution undefined`。

- [ ] **Step 3: 实现 Execution 类型和深拷贝**

```go
const (
	ExecutionLocal = "local"
	ExecutionWorker = "worker"
)

type Execution struct {
	Kind string `json:"kind"`
	WorkerBaseURL string `json:"worker_base_url,omitempty"`
}

type Profile struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Description string `json:"description,omitempty"`
	Tags []string `json:"tags,omitempty"`
	Command string `json:"command"`
	WorkDir string `json:"work_dir,omitempty"`
	Env map[string]string `json:"env,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
	Readiness Readiness `json:"readiness"`
	StopGraceSeconds int `json:"stop_grace_seconds"`
	LogBufferBytes int `json:"log_buffer_bytes"`
	Execution Execution `json:"execution"`
}
```

`Profile.Validate` 必须先规范空 Kind 为 `local` 的副本再校验，但 Repository Create/Update 在保存前调用 `Normalize`，确保磁盘不会继续写空 Kind。

- [ ] **Step 4: 实现显式 v1→v2 迁移并原子回写**

```go
const profileSchemaVersion = 2

func migrateProfileDocument(document *profileDocument) (bool, error) {
	switch document.SchemaVersion {
	case 1:
		for index := range document.Profiles {
			document.Profiles[index].Execution = Execution{Kind: ExecutionLocal}
		}
		document.SchemaVersion = 2
		return true, nil
	case 2:
		return false, nil
	default:
		return false, fmt.Errorf("backend profile schema version %d is unsupported", document.SchemaVersion)
	}
}
```

迁移成功后调用 `store.WriteJSON(path, document, 0o600)` 原子回写，再构造 Repository。

- [ ] **Step 5: 运行 Backend 测试和 Race**

Run: `gofmt -w internal/backend && go test -race ./internal/backend`

Expected: PASS，包括所有既有本地 Profile 测试。

- [ ] **Step 6: 提交 Profile 迁移**

```bash
git add internal/backend/profile.go internal/backend/profile_test.go internal/backend/repository.go internal/backend/repository_test.go
git commit -m "feat: configure backend execution location"
```

### Task 6: Backend Manager 的远端运行与状态对账

**Files:**
- Create: `internal/backend/remote_run.go`
- Create: `internal/backend/remote_run_test.go`
- Modify: `internal/backend/manager_linux.go`
- Modify: `internal/backend/manager_linux_test.go`

**Interfaces:**
- Consumes: `worker.Client`、`Profile.Execution` 和现有 `RunInfo` Web 语义。
- Produces: `NewManager(repository, logDir, WorkerClientFactory)`；Context-aware `Start(ctx, profileID, overrides)`；远端 Run 的 `WorkerInstanceID`/`WorkerRunID`/连接状态。

- [ ] **Step 1: 写远端路由、实例换代和断线失败测试**

```go
func TestManagerStartsWorkerProfileRemotely(t *testing.T) {
	fake := newFakeWorkerClient()
	repository := repositoryWithProfile(t, backend.Profile{
		Name: "Remote SD", Command: "sd-server --port ${PORT_SH}",
		Variables: map[string]string{"PORT": "8080"},
		Execution: backend.Execution{Kind: backend.ExecutionWorker, WorkerBaseURL: "http://127.0.0.1:8288"},
		Readiness: backend.Readiness{Kind: backend.ReadinessNone, TimeoutSeconds: 60},
		StopGraceSeconds: 10, LogBufferBytes: 1 << 20,
	})
	manager := backend.NewManager(repository, t.TempDir(), func(baseURL string) backend.WorkerClient { return fake })
	run, err := manager.Start(context.Background(), profileID(repository), map[string]string{"PORT": "8188"})
	if err != nil { t.Fatal(err) }
	if fake.startRequest.Command != "sd-server --port '8188'" { t.Fatalf("command = %q", fake.startRequest.Command) }
	if run.WorkerRunID == "" || run.WorkerInstanceID == "" { t.Fatalf("run = %#v", run) }
}
```

另测本机 Profile 不调用 Worker、Worker 忙、启动响应丢失后 Status 对账、Run ID 不匹配、Instance ID 改变后进入 `interrupted`/连接未知、隧道断开不标 stopped、远端终态、远端 Stop、远端日志快照/SSE、手动保存写入本机 `manual-<run-id>.log`、远端清屏只影响浏览器、Shutdown 有界停止。

- [ ] **Step 2: 运行测试并确认 Manager 构造和 Start 签名不匹配**

Run: `go test ./internal/backend -run 'TestManager.*Worker|TestRemoteRun'`

Expected: FAIL，错误包含 `too many arguments in call to backend.NewManager`。

- [ ] **Step 3: 定义 WorkerClient 接口与远端元数据**

```go
type WorkerClient interface {
	Health(context.Context) (worker.HealthResponse, error)
	Status(context.Context) (worker.StatusResponse, error)
	Start(context.Context, worker.StartRequest) (worker.Run, error)
	Stop(context.Context, string) (worker.Run, error)
	Logs(context.Context, string) (worker.LogSnapshot, error)
	SubscribeLogs(context.Context, string) (<-chan worker.LogEvent, <-chan error, error)
}

type WorkerClientFactory func(baseURL string) WorkerClient

type RunInfo struct {
	RunID string `json:"run_id"`
	ProfileID string `json:"profile_id"`
	ProfileName string `json:"profile_name"`
	State State `json:"state"`
	PID int `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	EndedAt *time.Time `json:"ended_at,omitempty"`
	ExitCode *int `json:"exit_code,omitempty"`
	Error string `json:"error,omitempty"`
	ProfileSnapshot Profile `json:"profile_snapshot"`
	ExecutionKind string `json:"execution_kind"`
	WorkerInstanceID string `json:"worker_instance_id,omitempty"`
	WorkerRunID string `json:"worker_run_id,omitempty"`
	ConnectionState string `json:"connection_state,omitempty"`
}
```

本地 Run 的 `ExecutionKind` 为 `local`；远端为 `worker`。连接状态只使用 `connected`、`unknown`，不混入进程 State。

- [ ] **Step 4: 把本机启动封装为原有路径并加入远端分支**

```go
func (manager *Manager) Start(ctx context.Context, profileID string, overrides map[string]string) (RunInfo, error) {
	profile, commandText, err := manager.resolveStart(profileID, overrides)
	if err != nil { return RunInfo{}, err }
	if profile.Execution.Kind == ExecutionWorker {
		return manager.startRemote(ctx, profile, commandText)
	}
	return manager.startLocal(profile, commandText)
}
```

远端 StartRequest 使用已经展开的命令和 Profile 快照。启动返回后立即保存 Worker 两个 ID，并运行一个有界后台状态对账循环；网络错误只更新 `connection_state=unknown` 和有限错误，不改变最后可信进程 State。实例变化或 Status Slot 不再匹配时把本地 Run 标记 `interrupted`，且后续 Stop 不发送到新 Slot。

- [ ] **Step 5: 实现远端日志代理与本机保存**

`LogSnapshot` 直接读取 Worker 当前快照；`SubscribeLog` 把远端 snapshot/chunk 转成现有字节流，若偏移断裂先重新读取快照。`SaveLog` 始终在工作台 `backends/crash-logs/manual-<local-run-id>.log` 写远端快照。`ClearLog` 对远端返回成功但不调用 Worker；前端负责清空当前 DOM。

- [ ] **Step 6: 更新全部调用点为 Context-aware Start**

```go
run, err := handler.manager.Start(request.Context(), id, input.Variables)
```

测试、Web Handler 和其他调用者都传入真实 Context；不使用 `context.Background()` 掩盖浏览器取消。

- [ ] **Step 7: 运行 Backend 与 Web 定向测试**

Run: `gofmt -w internal/backend internal/web/backend.go && go test -race ./internal/backend ./internal/web`

Expected: PASS，既有本机进程、日志和 API 行为不回归。

- [ ] **Step 8: 提交远端 Manager 集成**

```bash
git add internal/backend internal/web/backend.go internal/web/backend_test.go
git commit -m "feat: run backend profiles through remote workers"
```

### Task 7: Worker 连接 API、应用装配与响应式界面

**Files:**
- Modify: `internal/web/backend.go`
- Modify: `internal/web/backend_test.go`
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/server_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Consumes: Task 5 的 Profile JSON 与 Task 6 的统一 Manager。
- Produces: `POST /api/v1/backends/worker/test`；可编辑执行位置、测试连接和观察远端状态的原生 UI。

- [ ] **Step 1: 写连接测试 API 和页面结构失败测试**

```go
func TestBackendWorkerConnectionTest(t *testing.T) {
	workerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/health" { t.Fatalf("path = %q", request.URL.Path) }
		writeJSON(response, http.StatusOK, worker.HealthResponse{Status: "ok", Version: "test", InstanceID: "instance-1"})
	}))
	defer workerServer.Close()
	handler := newBackendTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/backends/worker/test", strings.NewReader(`{"worker_base_url":`+strconv.Quote(workerServer.URL)+`}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK { t.Fatalf("status = %d body = %s", response.Code, response.Body.String()) }
}
```

静态页面测试断言 `backend-execution-kind`、`backend-worker-url`、`backend-worker-test`、`backend-execution-summary` 存在，并且复杂字段位于 `<details>`。

- [ ] **Step 2: 运行 Web/App 测试并确认路由或元素缺失**

Run: `go test ./internal/web ./internal/app -run 'TestBackendWorker|Test.*Static|TestNewServer'`

Expected: FAIL，返回 404 或缺少元素。

- [ ] **Step 3: 实现有界连接测试路由并注入客户端工厂**

连接测试 Body 仅接受 `worker_base_url`，构造与 Manager 相同的客户端并用 3 秒 Context 调用 Health。成功返回：

```json
{"status":"ok","version":"dev","instance_id":"e2d5714ce0e64c5f9ba4be9b2a395d70"}
```

网络错误返回 `502 worker_unreachable`，协议版本/状态非法返回 `502 worker_invalid_response`，URL 校验错误返回 400。`app.newRuntime` 注入默认 `worker.Client` Factory；应用关闭继续经 `backend.Manager.Shutdown` 停止本机和可信匹配的远端 Run。

- [ ] **Step 4: 在折叠区域增加执行位置字段**

```html
<details class="advanced-fields">
  <summary>执行位置与高级设置</summary>
  <div class="form-grid">
    <label>执行位置
      <select id="backend-execution-kind">
        <option value="local">本机</option>
        <option value="worker">远端 Worker</option>
      </select>
    </label>
    <label id="backend-worker-url-field" hidden>Worker URL
      <input id="backend-worker-url" type="url" placeholder="http://127.0.0.1:8288">
    </label>
    <button type="button" class="secondary-button" id="backend-worker-test" hidden>测试连接</button>
    <p class="inline-message" id="backend-worker-test-result" aria-live="polite"></p>
  </div>
</details>
```

后端详情折叠区显示 `backend-execution-summary`、Worker Instance ID、Worker Run ID 和连接状态；主列表仅加短标签“本机”或“远端”，不展示 URL。

- [ ] **Step 5: 实现原生 JS 的 Profile 序列化和浏览器清屏**

```js
execution: document.querySelector("#backend-execution-kind").value === "worker"
  ? { kind: "worker", worker_base_url: document.querySelector("#backend-worker-url").value.trim() }
  : { kind: "local" },
```

切换执行位置时只切换相关字段 `hidden`。测试连接按钮 POST URL 并显示成功实例或必要错误。远端日志“清屏”只执行 `backendLogText = ""; renderBackendLog();`，不请求 Worker 清空；下一条 Chunk 继续显示，新建 SSE 时 Snapshot 会恢复 Worker 当前缓冲。

- [ ] **Step 6: 更新响应式样式与静态资源测试**

复用 `.form-grid`、`.advanced-fields` 和现有 720px 断点。Worker URL 行在桌面占两列，窄屏单列；按钮使用文字，不增加图标或第三方资源。

- [ ] **Step 7: 运行 Web、App 和全仓 Race 测试**

Run: `gofmt -w internal/web internal/app && go test -race ./...`

Expected: PASS。

- [ ] **Step 8: 提交应用与 UI 集成**

```bash
git add internal/app internal/web
git commit -m "feat: configure remote workers in backend ui"
```

### Task 8: 文档、真实二进制验收与阶段推送

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md`
- Modify: `docs/superpowers/specs/2026-08-31-remote-worker-design.md`
- Modify: `docs/superpowers/plans/2026-08-31-remote-worker.md`

**Interfaces:**
- Consumes: Tasks 1–7 的最终 CLI、API、界面和行为。
- Produces: 可复制的构建/运行命令、SSH 双隧道说明、限制、验收证据和已推送分支。

- [ ] **Step 1: 更新用户运行说明**

README 加入：

```bash
go build -o ai-worker ./cmd/ai-worker
./ai-worker --port 8288
```

以及控制面与模型数据面双隧道示例：

```bash
ssh \
  -L 8288:127.0.0.1:8288 \
  -L 8080:127.0.0.1:8080 \
  user@gpu-host
```

明确 Worker 无鉴权、只可通过 SSH、一次一个进程、无文件传输、无日志持久化；远端高级视频 CLI 不在本阶段。

- [ ] **Step 2: 更新阶段状态与计划勾选**

总体设计把阶段 6 标为已交付、阶段 7 视频待实施。Worker 规格状态改为“已交付”，记录真实 API/CLI 与限制。本计划将完成步骤勾为 `[x]`，不得改变尚未实现要求来迁就代码。

- [ ] **Step 3: 运行完整验证**

Run:

```bash
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/ai-workbench-check ./cmd/ai-workbench
go build -o /tmp/ai-worker-check ./cmd/ai-worker
git diff --check
```

Expected: 所有命令退出 0，无测试失败、Race、Vet 或空白错误。

- [ ] **Step 4: 运行真实 Worker HTTP/进程烟测**

用 `/tmp/ai-worker-check --port 18288` 启动 Worker，确认监听地址是 `127.0.0.1:18288`；调用 Health；POST 一个输出唯一 Marker 的短 Shell；轮询到终态；GET 日志确认 Marker 原文；再启动长进程并 STOP，确认终态。最后确认进程退出且无遗留 Fixture 进程。

- [ ] **Step 5: 审查需求映射和工作树**

Run:

```bash
rg -n 'T[O]DO|T[B]D|待确[认]|以后再[定]' README.md docs/superpowers/specs/2026-08-31-remote-worker-design.md docs/superpowers/plans/2026-08-31-remote-worker.md
git status --short
git diff --stat
```

Expected: 第一条无输出；状态只包含本任务文档更新，Diff 与规格一致。

- [ ] **Step 6: 提交阶段文档**

```bash
git add README.md docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md docs/superpowers/specs/2026-08-31-remote-worker-design.md docs/superpowers/plans/2026-08-31-remote-worker.md
git commit -m "docs: complete remote worker phase"
```

- [ ] **Step 7: 推送并核对远端提交**

```bash
git push origin HEAD
git status --short --branch
git rev-parse HEAD
git rev-parse @{upstream}
```

Expected: 工作树干净，两个提交哈希完全一致。
