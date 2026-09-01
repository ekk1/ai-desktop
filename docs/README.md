# 项目文档

本目录保存 AI Desktop v1.0 的稳定设计资料。日常安装、运行和功能说明以仓库根目录的 [README](../README.md) 为准；修改实现前，先阅读[总体设计](design/architecture.md)，再按涉及领域读取对应文档。

## 设计索引

- [总体架构与产品边界](design/architecture.md)
- [LLM 完整请求工作区](design/llm-workspace.md)
- [生图批次工作区](design/image-workspace.md)
- [视频工作区](design/video-workspace.md)
- [远端进程 Worker](design/remote-worker.md)

## v1.0 状态

最初计划的阶段 1–7 均已交付。Remote Worker 的文件传输、生成代理、远端视频 CLI 和多节点调度属于低优先级后续优化，不是 v1.0 欠项。

发布前的代码审查、普通测试、竞态测试、vet、发布构建和实际启动检查均已完成。由于验收环境没有浏览器，视频工作区在 1440px 与 390px 宽度下的人工视觉检查仍需在合适环境中补做。

已完成的逐任务实施计划不再保留在当前文档树中；需要追溯实现过程时使用 Git 历史。新的功能计划应只描述尚未交付的工作，完成后将仍有长期价值的约束合并回对应设计文档。
