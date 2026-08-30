# AI Desktop

一个面向 Linux 本地 AI 工作流的单体 Web 工作台。后端仅使用 Go 标准库，前端仅使用原生 HTML、CSS 和 JavaScript，最终构建为一个内嵌页面资源的二进制。

当前已完成基础骨架和后端管理：

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

共享资产、LLM 请求、图像和视频生成将在后续阶段按[总体设计](docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md)逐步接入。

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

工作台只监听本机回环地址，并且不提供鉴权。不要通过反向代理直接暴露到公网。

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
