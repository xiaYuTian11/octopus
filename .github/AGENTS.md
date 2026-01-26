# AGENTS

本文件为在 `.github` 目录工作的 Agent 提供快速指引，请遵守以下规则：

## 基本行为
- 交流语言保持简体中文；输出时简洁，引用文件请带路径和行号。
- 修改前先快速阅读关联文件（issue 模板与 workflows），确认用户意图，不要清理/重置他人改动。
- 严禁泄露或打印任何机密（如 `ACTION_TOKEN`、`DOCKERHUB_*`、`GITHUB_TOKEN`）；调试时勿回显 secrets。
- 优先最小化变更，保持分支名/触发条件一致，必要时在说明中指出风险和需要的密钥。

## 目录速览
- `ISSUE_TEMPLATE/`: `bug.yml`、`feature.yml`、`config.yml`。
- `workflows/`: `release.yaml`、`changelog.yml`、`sync-tags.yml`。

## 主要工作流注意事项
- `release.yaml`：在 `master` 分支 push 时触发，执行 `scripts/build.sh release` 后基于最新 tag 发布并推送 Docker 镜像。依赖 Go、Python、pnpm/Node 与 Docker Buildx/QEMU。修改时保持 `build/archives/*` 输出路径、tag 获取逻辑、镜像标签格式，勿更改密钥引用。
- `changelog.yml`：匹配 `v*` tag 时运行 `changelogithub` 并将 `dev` 合并到 `master`（强制推送）。调整流程时注意避免破坏 tag 语义或误改分支历史。
- `sync-tags.yml`：在 `dev` push、定时或手动触发，同步上游 `https://github.com/bestruirui/octopus.git` 的 tags。修改时确保不会删除现有 remote/tag。

## 修改与验证建议
- 对工作流改动，优先给出预期效果与潜在影响；如需测试，使用 dry-run 或说明本地验证思路，避免直接执行破坏性操作。
- 若新增工作流或模板，保持与现有分支策略（`dev`/`master`、tag 规则）一致，并在总结中提示触发条件和所需 secrets。
