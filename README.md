# AI Desktop

一个面向 Linux 本地 AI 工作流的单体 Web 工作台。后端仅使用 Go 标准库，前端仅使用原生 HTML、CSS 和 JavaScript，最终构建为一个内嵌页面资源的二进制。

当前完成的是第一阶段基础骨架：

- 固定监听 `127.0.0.1` 的 HTTP Server
- 可覆盖的数据目录和监听端口
- 同一数据目录的 Linux 排他实例锁
- 带版本号、备份和原子替换的 JSON 配置存储
- `/api/v1/health` 与 `/api/v1/settings`
- 桌面双栏、窄屏抽屉式的响应式工作台 Shell
- LLM、生图、视频、后端管理、Gallery、知识库和配置入口

后端进程管理、共享资产、LLM 请求、图像和视频生成将在后续阶段按[总体设计](docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md)逐步接入。

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
