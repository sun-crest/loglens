# LogLens — Claude 开发约定

项目：本地日志工作台 + 内置 MCP server（`github.com/sun-crest/loglens`）。
架构 / 需求 / 目录树部署见 `docs/DESIGN.md`、`docs/REQUIREMENTS.md`、README；本文只列**代理行为硬约束**。

## 版本号（硬约束）

- **唯一真相源**：`cmd/loglens/main.go` 的 `const version`。它同时驱动 `loglens version`、MCP `Implementation.Version`、发行包名，三处不得分叉。
- **开发期版本必须带 `-dev` 后缀**（如 `0.2.0-dev`）；不要随手改成纯数字版本。
- **打自包含包**：`./scripts/package.sh`，接受任意合法 semver（含 `-dev`），用途是本地冒烟 / 测试机部署。
- **发版**：只能走 `./scripts/release.sh`，顺序是 `--dry-run` 预览 → 用户确认 → 再执行。
- **不做**：不代打 git tag、不代跑 `gh release create`、不绕过 `release.sh` 手动发布——这些都是改动远端 / 不可逆动作，先停下来汇报，等用户确认。
- 完整版本管理与发版说明：`docs/RELEASING.md`。

## 常规操作

- 测试：`go test ./... -race`；构建：`go build ./cmd/loglens`。完整开发 / 测试 / 冒烟说明：`docs/DEVELOPMENT.md`。
- 提交信息用 `<类型>: <中文摘要>` 前缀（`feat:` / `fix:` / `refactor:` / `docs:` / `chore:`）。变更记录由 GitHub Release Notes（`--generate-notes`）承担，**不建独立 CHANGELOG.md**，见 `docs/RELEASING.md`「变更记录」。
- 改到版本 / 发版脚本 / 版本文档相关代码时，先读 `docs/RELEASING.md`，改动后与 `scripts/release.sh` 注释保持一致。