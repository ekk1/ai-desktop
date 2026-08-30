# 基础骨架实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 交付一个只依赖 Go 标准库、可在 Linux 启动、内嵌响应式浏览器界面并具备可靠文件存储和版本化 API 的工作台骨架。

**Architecture:** 单个 `cmd/ai-workbench` 程序解析数据目录和端口，获取 Linux 实例锁，加载版本化配置，再启动 `net/http` Server。业务基础能力分布于 `internal/config`、`internal/store`、`internal/instance`、`internal/web` 和 `internal/app`，前端资源由 `internal/web` 使用 `go:embed` 嵌入。

**Tech Stack:** Go 1.24、Go 标准库、原生 HTML/CSS/JavaScript、`go:embed`

**Spec:** `docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md`

## Global Constraints

- 应用是单个 Go 二进制，只使用 Go 标准库。
- Server 只在 Linux 上运行，并且只监听 `127.0.0.1`。
- 浏览器端不使用第三方依赖、CDN、包管理器或构建工具。
- 持久化文件必须带 `schema_version`，重要写入使用临时文件、`fsync` 和原子重命名。
- API Key 最终直接保存在 `0600` 的主配置文件中；本阶段先建立相同权限的配置容器。
- 所有自动化测试只使用 Go 标准库。

---

## 文件结构

```text
go.mod                              Go Module 与 Go 版本
cmd/ai-workbench/main.go            进程入口
internal/app/app.go                 参数解析后的应用启动和优雅关闭
internal/app/app_test.go            应用装配与监听地址测试
internal/config/config.go           配置结构、默认值、校验和持久化
internal/config/config_test.go      配置默认值、迁移和校验测试
internal/store/json.go              原子 JSON 读写和备份恢复
internal/store/json_test.go         存储故障与权限测试
internal/instance/lock_linux.go     Linux 数据目录排他锁
internal/instance/lock_linux_test.go 锁竞争测试
internal/web/server.go              路由、Middleware、静态资源和 API 装配
internal/web/server_test.go         API、错误 Envelope 和静态资源测试
internal/web/static/index.html      响应式应用 Shell
internal/web/static/styles.css      无依赖布局和视觉样式
internal/web/static/app.js          原生模块切换和健康状态读取
README.md                           构建、运行、SSH 隧道和验证说明
```

### Task 1: 建立 Module、版本化配置与原子 JSON Store

**Files:**
- Create: `go.mod`
- Create: `internal/store/json.go`
- Create: `internal/store/json_test.go`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

**Interfaces:**
- Produces: `store.WriteJSON(path string, value any, perm fs.FileMode) error`
- Produces: `store.ReadJSON(path string, value any) error`
- Produces: `config.ResolveDataDir(explicit string) (string, error)`
- Produces: `config.Load(path string) (config.Config, error)`
- Produces: `config.Save(path string, cfg config.Config) error`
- Produces: `config.Config.Validate() error`

- [x] **Step 1: 创建 Go Module，并写 Store 失败测试**

`go.mod` 使用：

```go
module github.com/ekk1/ai-desktop

go 1.24.0
```

测试覆盖：首次写入可读取、替换后产生 `.bak`、目标权限为 `0600`、损坏主文件可显式从 `.bak` 读取。测试全部使用 `t.TempDir()`，示例断言：

```go
type document struct {
    SchemaVersion int    `json:"schema_version"`
    Name          string `json:"name"`
}

path := filepath.Join(t.TempDir(), "config.json")
if err := WriteJSON(path, document{SchemaVersion: 1, Name: "first"}, 0o600); err != nil {
    t.Fatal(err)
}
var got document
if err := ReadJSON(path, &got); err != nil {
    t.Fatal(err)
}
if got.Name != "first" {
    t.Fatalf("Name = %q", got.Name)
}
```

- [x] **Step 2: 运行 Store 测试并确认失败**

Run: `go test ./internal/store -run Test -v`

Expected: FAIL，原因是 `WriteJSON` 和 `ReadJSON` 尚未定义。

- [x] **Step 3: 实现最小原子 JSON Store**

`WriteJSON` 必须在目标目录创建临时文件，设置权限，使用带缩进的 `json.Encoder` 写入，调用 `Sync` 和 `Close`，在旧文件存在时复制为 `.bak`，最后使用 `os.Rename` 替换。所有错误包含操作和路径上下文。`ReadJSON` 使用 `DisallowUnknownFields` 不合适，因为迁移期间需容忍新增字段；它只检查尾部不存在第二个 JSON 值。

- [x] **Step 4: 写配置失败测试**

配置结构固定为：

```go
const CurrentSchemaVersion = 1

type Config struct {
    SchemaVersion          int `json:"schema_version"`
    ListenPort             int `json:"listen_port"`
    ShutdownTimeoutSeconds int `json:"shutdown_timeout_seconds"`
    MaxUploadBytes         int64 `json:"max_upload_bytes"`
}
```

默认值为端口 `8188`、关闭超时 `10` 秒、上传上限 `268435456` 字节。测试验证显式目录转成绝对路径；未指定目录时优先使用 `XDG_DATA_HOME/ai-workbench`，否则使用 `$HOME/.local/share/ai-workbench`；端口必须在 `1..65535`，超时必须在 `1..300`，上传上限必须在 `1 MiB..16 GiB`。

- [x] **Step 5: 运行配置测试并确认失败**

Run: `go test ./internal/config -run Test -v`

Expected: FAIL，原因是配置函数尚未定义。

- [x] **Step 6: 实现配置加载、默认创建、校验和保存**

`Load` 在文件不存在时返回默认配置并调用 `Save`；已有文档版本大于当前版本时返回明确错误；版本小于当前版本时通过显式迁移函数逐级升级。版本 1 没有历史迁移，但保留 `migrate(Config) (Config, error)` 边界。配置始终通过 `store.WriteJSON(..., 0o600)` 保存。

- [x] **Step 7: 运行 Task 1 测试**

Run: `go test ./internal/store ./internal/config -v`

Expected: PASS。

- [x] **Step 8: 提交 Task 1**

```bash
git add go.mod internal/store internal/config
git commit -m "feat: add versioned file storage"
```

### Task 2: Linux 数据目录实例锁

**Files:**
- Create: `internal/instance/lock_linux.go`
- Create: `internal/instance/lock_linux_test.go`

**Interfaces:**
- Produces: `instance.Acquire(path string) (*instance.Lock, error)`
- Produces: `(*instance.Lock).Close() error`

- [x] **Step 1: 写实例锁失败测试**

测试在同一路径调用两次 `Acquire`，第二次必须返回包含 `already in use` 的错误；关闭第一次锁之后，再次获取必须成功。锁使用 `<data-dir>/instance.lock`，文件中写当前 PID 供诊断。

- [x] **Step 2: 运行锁测试并确认失败**

Run: `go test ./internal/instance -run TestAcquire -v`

Expected: FAIL，原因是 `Acquire` 尚未定义。

- [x] **Step 3: 使用标准库 `syscall.Flock` 实现 Linux 排他锁**

打开锁文件后调用：

```go
if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
    file.Close()
    return nil, fmt.Errorf("data directory already in use: %w", err)
}
```

成功后截断并写入 PID。`Close` 先 `LOCK_UN` 再关闭文件，并保证重复调用安全。

- [x] **Step 4: 运行 Task 2 测试**

Run: `go test ./internal/instance -v`

Expected: PASS。

- [x] **Step 5: 提交 Task 2**

```bash
git add internal/instance
git commit -m "feat: lock the workbench data directory"
```

### Task 3: 版本化 HTTP API 与嵌入式静态服务

**Files:**
- Create: `internal/web/server.go`
- Create: `internal/web/server_test.go`
- Create: `internal/web/static/index.html`
- Create: `internal/web/static/styles.css`
- Create: `internal/web/static/app.js`

**Interfaces:**
- Consumes: `config.Config`
- Produces: `web.Options{Version string, DataDir string, Config config.Config}`
- Produces: `web.NewHandler(opts web.Options) http.Handler`
- Produces endpoint: `GET /api/v1/health`
- Produces endpoint: `GET /api/v1/settings`
- Produces embedded assets: `/`, `/assets/styles.css`, `/assets/app.js`

- [x] **Step 1: 写 HTTP 失败测试**

测试使用 `httptest.NewRecorder` 验证：

```go
req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
rr := httptest.NewRecorder()
handler.ServeHTTP(rr, req)
if rr.Code != http.StatusOK {
    t.Fatalf("status = %d", rr.Code)
}
```

健康响应必须为：

```json
{"status":"ok","version":"test"}
```

设置响应包含配置值和数据目录；不存在的 API 返回 `404` JSON Envelope：

```json
{"error":{"code":"not_found","message":"resource not found"}}
```

错误方法返回 `405`，响应带 `Content-Type: application/json`。根页面和两个静态资源返回 `200`，未知浏览器路径返回 `404`，不做隐式 SPA Fallback。

- [x] **Step 2: 运行 Web 测试并确认失败**

Run: `go test ./internal/web -run Test -v`

Expected: FAIL，原因是 `NewHandler` 尚未定义。

- [x] **Step 3: 实现路由、错误 Envelope 和安全响应头**

使用 Go 1.24 `http.ServeMux` 方法路由。公共 Middleware 设置：

```text
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
```

API JSON 使用 `json.Encoder`，禁止输出 HTML 错误页。静态资源通过 `//go:embed static/*` 嵌入并以显式路径提供；`index.html` 使用 `Cache-Control: no-cache`，带内容哈希以前的 CSS/JS 同样不做长期缓存。

- [x] **Step 4: 运行 Task 3 测试**

Run: `go test ./internal/web -v`

Expected: PASS。

- [x] **Step 5: 提交 Task 3**

```bash
git add internal/web
git commit -m "feat: serve embedded workbench API"
```

### Task 4: 应用生命周期与程序入口

**Files:**
- Create: `internal/app/app.go`
- Create: `internal/app/app_test.go`
- Create: `cmd/ai-workbench/main.go`

**Interfaces:**
- Consumes: `config.ResolveDataDir`, `config.Load`, `instance.Acquire`, `web.NewHandler`
- Produces: `app.Options{DataDir string, PortOverride int, Version string}`
- Produces: `app.Run(ctx context.Context, opts app.Options) error`

- [x] **Step 1: 写应用装配失败测试**

把 Server 构造边界定义为 `app.NewServer(dataDir string, cfg config.Config, version string, portOverride int) (*http.Server, error)`。测试验证 `server.Addr` 永远是 `127.0.0.1:<port>`，端口覆盖只改变端口，非法覆盖返回错误，Handler 能返回健康响应。

- [x] **Step 2: 运行应用测试并确认失败**

Run: `go test ./internal/app -run TestNewServer -v`

Expected: FAIL，原因是 `NewServer` 尚未定义。

- [x] **Step 3: 实现应用启动和优雅退出**

`Run` 的顺序为：解析数据目录、创建目录、获取实例锁、加载配置、应用合法端口覆盖、创建 Handler、启动 `http.Server`、等待 Context 取消或监听错误、使用配置超时调用 `Shutdown`。只允许拼接 `127.0.0.1` 和数值端口，不接受任意监听主机。

`app` 只构造 `web.Options` 并调用 `web.NewHandler`，不直接注册业务路由。Server 设置 `ReadHeaderTimeout: 5s`、`IdleTimeout: 60s`，不设置全局 `WriteTimeout`，为后续 SSE 长连接保留空间。

入口使用标准库 `flag`，支持 `--data-dir`、`--port` 和 `--version`；使用 `signal.NotifyContext` 监听 `SIGINT` 与 `SIGTERM`。版本通过构建变量注入，默认 `dev`。

- [x] **Step 4: 运行 Task 4 测试**

Run: `go test ./internal/app -v`

Expected: PASS。

- [x] **Step 5: 提交 Task 4**

```bash
git add cmd/ai-workbench internal/app
git commit -m "feat: add linux application lifecycle"
```

### Task 5: 原生响应式工作台 Shell

**Files:**
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/server_test.go`

**Interfaces:**
- Consumes: `GET /api/v1/health`, `GET /api/v1/settings`
- Produces: 顶部模块导航、响应式左侧栏、模块占位工作区、连接状态和运行信息

- [x] **Step 1: 扩展静态资源契约测试并确认失败**

测试读取嵌入的根页面并断言包含七个导航目标的稳定 `data-module` 值：`llm`、`images`、`video`、`backends`、`gallery`、`knowledge`、`settings`；还必须包含 `aria-controls="workspace-sidebar"` 和页面主标题挂载点。测试 CSS 响应中含 `@media (max-width: 760px)`，JavaScript 响应使用 `fetch("/api/v1/health")`。

Run: `go test ./internal/web -run TestEmbedded -v`

Expected: FAIL，因为完整页面契约尚未实现。

- [x] **Step 2: 实现语义化 HTML Shell**

页面结构固定为：顶部 `<header>` 与 `<nav>`、包含 `<aside id="workspace-sidebar">` 和 `<main>` 的工作区、连接状态区。所有导航和按钮使用文字，只有移动端侧栏开关使用简短符号并带可见文字或 `aria-label`。每个未实现模块显示清晰的阶段说明，不模拟尚不存在的功能。

- [x] **Step 3: 实现响应式 CSS**

桌面使用两列 Grid，左栏宽度限制在 `15rem..20rem`，主区最小宽度为零以避免横向溢出。`760px` 以下左栏变成固定抽屉、主区单列、顶部导航横向滚动。所有颜色、间距、圆角通过 CSS 自定义属性集中定义；支持 `prefers-color-scheme`，不引入主题配置。

- [x] **Step 4: 实现无框架 JavaScript**

ES Module 负责：模块按钮切换、更新标题和说明、移动端侧栏开关、Escape 关闭侧栏、调用健康与设置 API、在页面显示版本/端口/数据目录以及连接失败状态。切换仅改变本地 DOM，不引入客户端路由框架。

- [x] **Step 5: 运行 Task 5 测试**

Run: `go test ./internal/web -v`

Expected: PASS。

- [x] **Step 6: 提交 Task 5**

```bash
git add internal/web/static internal/web/server_test.go
git commit -m "feat: add responsive browser shell"
```

### Task 6: 集成验证与使用文档

**执行记录：** 生命周期集成测试在 Task 4 与 `app.Run` 同步完成，以保持测试先于实现；Task 6 重新运行该测试并完成 README 与阶段构建验证。

**Files:**
- Create: `README.md`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Consumes: 完整可执行程序与健康接口
- Produces: 可复现的构建、运行、SSH 隧道和验证步骤

- [x] **Step 1: 添加端到端生命周期测试**

测试使用空闲本地端口和临时数据目录启动 `app.Run`，轮询 `http://127.0.0.1:<port>/api/v1/health` 直到成功，取消 Context，并断言 `Run` 在两秒内无错误退出。测试同时验证配置文件权限为 `0600`，数据目录包含 `instance.lock`。

- [x] **Step 2: 运行生命周期测试并修复集成问题**

Run: `go test ./internal/app -run TestRunLifecycle -count=1 -v`

Expected: PASS；不得跳过或依赖固定端口。

- [x] **Step 3: 编写 README**

README 必须给出：

```bash
go build -o ai-workbench ./cmd/ai-workbench
./ai-workbench --data-dir ./workbench-data --port 8188
ssh -L 8188:127.0.0.1:8188 user@linux-host
go test ./...
```

并明确：只监听本机、无鉴权、SSH 隧道访问、零第三方 Go/前端依赖、当前实现范围仅为第一阶段基础骨架。

- [x] **Step 4: 执行完整验证**

Run: `gofmt -w cmd internal`

Run: `go vet ./...`

Expected: PASS，无输出。

Run: `go test ./... -count=1`

Expected: PASS。

Run: `go build ./cmd/ai-workbench`

Expected: PASS，并生成可执行文件；验证后删除该构建产物，避免提交二进制。

Run: `git diff --check`

Expected: PASS，无输出。

- [x] **Step 5: 提交 Task 6**

```bash
git add README.md internal/app/app_test.go
git commit -m "docs: add workbench usage guide"
```

### Task 7: 阶段验收与推送

**Files:**
- Modify: `docs/superpowers/plans/2026-08-30-foundation.md`

**Interfaces:**
- Consumes: Task 1–6 的提交和验证结果
- Produces: 已勾选计划、干净工作树和远端分支

- [x] **Step 1: 将实际完成的计划项勾选为 `[x]`**

只勾选已由测试或检查验证的步骤；若实现与计划存在差异，在对应任务下写明最终接口和原因。

- [x] **Step 2: 重新执行阶段验收**

Run: `go vet ./... && go test ./... -count=1 && git diff --check`

Expected: PASS。

- [x] **Step 3: 提交计划执行状态**

```bash
git add docs/superpowers/plans/2026-08-30-foundation.md
git commit -m "docs: record foundation delivery"
```

- [x] **Step 4: 推送主分支**

Run: `git push origin main`

Expected: 本地 `main` 与 `origin/main` 指向同一最新提交。
