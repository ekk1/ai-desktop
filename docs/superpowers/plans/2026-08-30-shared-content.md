# 共享资产与知识备忘录实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付跨模块共享的内容寻址资产库、active/archive 精选流、Gallery，以及可被后续 LLM 请求引用的轻量知识备忘录。

**Architecture:** `internal/asset` 独立管理受控文件与元数据索引；`internal/knowledge` 只保存文本条目和资产 ID，不直接操作媒体文件。两者分别通过 `internal/web` 暴露 API，浏览器在 Gallery 与知识库模块中提供独立界面。

**Tech Stack:** Go 1.24 标准库、SHA-256、multipart HTTP、原生 HTML/CSS/JavaScript

**Spec:** `docs/superpowers/specs/2026-08-30-local-ai-workbench-design.md`

## Global Constraints

- 新导入和新生成资产默认 `archive`，只有 `active` 出现在其他模块选择器。
- 文件名只用于显示；真实路径由内容哈希和受控扩展名生成。
- 元数据保存稳定资产 ID、SHA-256、媒体类型、来源、备注和引用。
- 被引用资产禁止物理删除，但允许归档。
- 知识库只做文件夹、标题、纯文本、标签和资产引用，不实现 RAG、embedding 或自动召回。
- 所有写入继续使用标准库、版本化 JSON 和原子替换。

---

### Task 1: 资产模型、内容导入与 Repository

**Files:**
- Create: `internal/asset/asset.go`
- Create: `internal/asset/asset_test.go`
- Create: `internal/asset/repository.go`
- Create: `internal/asset/repository_test.go`

**Interfaces:**
- Produces: `asset.Asset`, `asset.State`, `asset.Reference`, `asset.ImportInput`
- Produces: `asset.OpenRepository(indexPath, filesDir string)`
- Produces: `Import`, `List`, `Get`, `SetState`, `UpdateMetadata`, `AddReference`, `RemoveReference`, `Delete`, `OpenContent`

- [ ] **Step 1: 写模型、导入与去重失败测试**

测试使用真实临时文件：导入后默认 archive、ID 稳定存在、SHA-256 正确、显示名不参与真实路径、PNG/JPEG 可通过 `image.DecodeConfig` 获取尺寸、同内容只产生一个物理文件但保留两个资产记录。

- [ ] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/asset -run 'Test(Import|Asset)' -v`

- [ ] **Step 3: 实现安全导入**

导入先流式写到 `tmp` 并计算哈希与字节数，再原子移动到 `<files-dir>/<sha256><ext>`。扩展名来自受限 MIME 映射或经过净化的原扩展名；绝不拼接用户目录。失败时清理临时文件。

- [ ] **Step 4: 写状态、引用与删除失败测试**

测试 active/archive 切换、元数据修改、添加/移除唯一引用、引用存在时 Delete 返回 `ErrReferenced`、无引用时同时移除资产记录并仅在最后一个内容记录删除物理文件。

- [ ] **Step 5: 实现 Repository 并发与持久化**

文档包含 `schema_version: 1` 和资产列表；返回值全部深拷贝，列表支持状态、媒体类型和文本过滤并按创建时间倒序。

- [ ] **Step 6: 运行并提交 Task 1**

Run: `go test ./internal/asset -count=1 -v`

```bash
git add internal/asset
git commit -m "feat: add shared asset repository"
```

### Task 2: 资产 HTTP API

**Files:**
- Create: `internal/web/asset.go`
- Create: `internal/web/asset_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/app/app.go`

**Interfaces:**
- Produces: `GET/POST /api/v1/assets`
- Produces: `GET/PATCH/DELETE /api/v1/assets/{id}`
- Produces: `GET /api/v1/assets/{id}/content`
- Produces: `POST /api/v1/assets/{id}/state`

- [ ] **Step 1: 写上传、查询、状态和内容下载失败测试**

通过 `multipart.Writer` 上传真实 PNG Fixture，验证默认 archive、状态过滤、active 切换、正确 Content-Type、下载原始字节、备注修改和路径穿越文件名不影响存储路径。

- [ ] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/web -run TestAsset -v`

- [ ] **Step 3: 实现 API 与 App 装配**

上传受 `MaxUploadBytes` 限制；JSON 修改严格解码；下载使用受控 Repository File，不接收磁盘路径；App 打开 `<data-dir>/assets/index.json` 与 `files/`。

- [ ] **Step 4: 运行并提交 Task 2**

Run: `go test ./internal/web ./internal/app -count=1 -v`

```bash
git add internal/web internal/app
git commit -m "feat: expose shared asset API"
```

### Task 3: Gallery 浏览器界面

**Files:**
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/server_test.go`

- [ ] **Step 1: 写 Gallery 页面契约失败测试**

验证导入控件、全部/active/archive 筛选、响应式媒体网格、预览层、精选切换、备注和删除操作挂载点。

- [ ] **Step 2: 实现并验证 Gallery**

图片使用受控内容 URL；视频使用原生 `<video controls preload="metadata">`；其他附件显示文件信息。删除冲突展示引用明细，不静默失败。

Run: `go test ./internal/web -v`

- [ ] **Step 3: 提交 Task 3**

```bash
git add internal/web/static internal/web/server_test.go
git commit -m "feat: add shared asset gallery"
```

### Task 4: 知识备忘录 Repository 与 API

**Files:**
- Create: `internal/knowledge/note.go`
- Create: `internal/knowledge/repository.go`
- Create: `internal/knowledge/repository_test.go`
- Create: `internal/web/knowledge.go`
- Create: `internal/web/knowledge_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/app/app.go`

**Interfaces:**
- Produces: `knowledge.Note{ID, Folder, Title, Content, Tags, AssetIDs, CreatedAt, UpdatedAt}`
- Produces: Note CRUD、文件夹与文本过滤
- Produces: `/api/v1/knowledge` 与 `/api/v1/knowledge/{id}`

- [ ] **Step 1: 写 Repository 失败测试**

测试创建、更新、删除、重新打开、文件夹/标签/正文搜索、稳定排序和深拷贝；标题必填，正文允许空，资产 ID 去重。

- [ ] **Step 2: 实现 Repository 并验证**

Run: `go test ./internal/knowledge -v`

- [ ] **Step 3: 写并实现 API 测试**

验证 CRUD、过滤参数、无效 JSON 和未知 ID 的 Error Envelope；App 打开 `<data-dir>/knowledge/notes.json`。

Run: `go test ./internal/web ./internal/app -count=1 -v`

- [ ] **Step 4: 提交 Task 4**

```bash
git add internal/knowledge internal/web internal/app
git commit -m "feat: add knowledge memo API"
```

### Task 5: 知识库浏览器界面

**Files:**
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/styles.css`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/server_test.go`

- [ ] **Step 1: 写知识库页面契约失败测试**

验证文件夹/条目列表、搜索、新建、标题、文件夹、标签、正文、资产 ID 和保存/删除操作。

- [ ] **Step 2: 实现简洁双栏备忘录 UI**

左侧模块栏显示文件夹与条目，右侧直接编辑当前条目；窄屏保持现有抽屉规则，保存状态就地显示。

- [ ] **Step 3: 运行并提交 Task 5**

Run: `go test ./internal/web -v`

```bash
git add internal/web/static internal/web/server_test.go
git commit -m "feat: add knowledge memo workspace"
```

### Task 6: 阶段验证、文档与推送

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-08-30-shared-content.md`

- [ ] **Step 1: README 增加资产状态、导入目录和知识库说明**

- [ ] **Step 2: 执行完整验证**

Run: `gofmt -w cmd internal && go vet ./... && go test ./... -count=1`

Run: `go test -race ./internal/asset ./internal/knowledge ./internal/web ./internal/app -count=1`

Run: `go build ./cmd/ai-workbench && git diff --check`

- [ ] **Step 3: 更新计划、提交并推送**

```bash
git add README.md docs/superpowers/plans/2026-08-30-shared-content.md
git commit -m "docs: record shared content delivery"
git push origin main
```

- [ ] **Step 4: 核对 `HEAD == origin/main` 且工作树干净**
