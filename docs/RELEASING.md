# LogLens 版本管理与发版

本文档是版本号与发版流程的**权威说明**；`scripts/release.sh` 是同一流程的自动化执行。
维护动作（bump、打 tag、发 release）对 Claude 代理的硬约束见 `.claude/CLAUDE.md`（每次会话自动加载）。

## 版本号

**唯一真相源**：`cmd/loglens/main.go` 的 `const version`。它同时驱动三处输出，任何情况下不得分叉：

- `loglens version`（及帮助信息）；
- MCP 服务器的 `Implementation.Version`（`internal/mcp/server.go`）；
- 发行包产物名 `loglens-<版本>-linux-amd64.tar.gz`。

遵循 [semver](https://semver.org)：`MAJOR.MINOR.PATCH`。0.x 阶段约定：

| 分量 | 何时升 | 含义 |
|---|---|---|
| MAJOR | （0.x 阶段不动） | 1.0 前的破坏性红线 |
| MINOR | 一次里程碑 | M1 → M2 → M3 迭代，如 0.2 → 0.3 |
| PATCH | bugfix / 微调 | 里程碑内的小修，如 0.2.1 → 0.2.2 |

## 开发态与发布态

| 时机 | `const version` | 说明 |
|---|---|---|
| 开发期 | `0.3.0-dev` | 下一个待发版本 + `-dev` 后缀，`loglens version` 一眼看出非发布态 |
| 发布态 | `0.3.0` | 纯版本号，配合 annotated tag `v0.3.0` |
| 发布后 | 立刻 bump 为 `0.4.0-dev` | 推进规则见下 |

**bump 推进规则**（`release.sh` 自动执行）：发布 `x.y.0` → 下一开发版本 `x.(y+1).0-dev`；发布 `x.y.z`（z>0）→ `x.y.(z+1)-dev`。

**开发期打自包含包完全合法**：`package.sh` 接受任意合法 semver（含 `-dev` / `-rc.x`），产物名即版本号，用于本地冒烟、部署测试机验证。**对外发布才把关版本**（见下）。

## 职责划分

| 工具 | 职责 | 对版本号的态度 |
|---|---|---|
| `scripts/package.sh` | 纯构建：任意合法 semver → 自包含 tar.gz + sha256 + 冒烟 | 只拦**格式非法**（非 semver） |
| `scripts/release.sh` | 一键发版：校验 → 发 GitHub release → 推进下一版本 | **拦截**带 `-` 的开发版本，只放行纯版本号 |

发布把关只在 `release.sh`：打包永远可以，对外发 release 才是需要校验的事。

## 一键发版

```bash
./scripts/release.sh            # 交互确认后执行（发版动作不可逆）
./scripts/release.sh --dry-run  # 只打印将执行的步骤，不做任何改动
./scripts/release.sh --yes      # 跳过确认提示（CI / 已确信）
```

前置条件（脚本逐一校验并给出指引，缺一步即停下）：

1. git 工作区干净；
2. `const version` 已是**纯版本号**（不带 `-dev`）；
3. annotated tag `v<版本>` 已打到 HEAD（脚本**不代打 tag**）；
4. `gh` 已认证（`gh auth status`）。

脚本执行序列：推送 `v<版本>` → `package.sh` 打包 → `gh release create --generate-notes` → bump main.go 到下一 `-dev` 并提交。

### 手动等价流程

适合未装 gh 或想逐步执行的情况，与 `release.sh` 行为一致：

```bash
# 1. bump：main.go 常量 0.3.0-dev -> 0.3.0
git add cmd/loglens/main.go
git commit -m "chore: bump version to 0.3.0"

# 2. 打 annotated tag
git tag -a v0.3.0 -m "loglens 0.3.0"

# 3. 打包 + 冒烟自校验
./scripts/package.sh   # -> dist/loglens-0.3.0-linux-amd64.tar.gz + .sha256

# 4. 发布
gh release create v0.3.0 \
  dist/loglens-0.3.0-linux-amd64.tar.gz \
  dist/loglens-0.3.0-linux-amd64.tar.gz.sha256 \
  --generate-notes

# 5. 发布后：main.go bump 为 0.4.0-dev，提交
```

## 发行产物

`scripts/package.sh` 产出 `dist/loglens-<版本>-linux-amd64.tar.gz` 及 `.sha256`，解包冒烟校验二进制 `version` 必须与包名一致（否则报错退出）；不接受版本参数，防止包名与二进制版本脱节。

包内容即目录树工作区：`bin/loglens` + `config/loglens.toml.example` + 空 `logs/`，目标机解包后 `./bin/loglens serve` 直接可用。脚本无交互、无硬编码目录，将来接 GitHub Actions / goreleaser 时作为构建打包步骤直接复用。

## tag 约定

- 命名：`vX.Y.Z`（与 `const version` 对齐）；
- 用 **annotated tag**（`git tag -a`）：携带打标说明，`git describe` 等工具行为更可预测；
- 必须指向发布提交（bump 到纯版本的那个 commit），`release.sh` 校验指向 HEAD。

## 变更记录（Changelog 策略）

**不维护独立的 CHANGELOG.md**——版本变更清单由**每次 GitHub Release 的 Release Notes** 承担：`release.sh` 用 `--generate-notes` 自动汇总上一个 tag 到本次发布之间的提交。前提是提交信息遵循 `<类型>: <中文摘要>` 传统前缀（`feat:` / `fix:` / `refactor:` / `docs:` / `chore:`），Release Notes 才有可读的结构；bump 与发版相关提交用 `chore: bump version to x.y.z`。

将来若出现需要离线阅读 / 面向外部用户的变更记录（多使用者、多平台发行、需在提交前评审变更清单），再引入 Keep-a-Changelog 约定并把 `--generate-notes` 换成 `--notes-file`——那是显式决策，不是默认缺失。

## 常见问题

- **想开发期部署一版到测试机 / 远程验证？** 保持 `-dev` 直接跑 `./scripts/package.sh`，产物名带 `-dev`，不会与正式版混淆。
- **想发预发布（rc）？** 目前 `release.sh` 拦截一切含 `-` 的版本。需要时给它加 `--prerelease` 分流到 `gh release create --prerelease`——这是显式决策，而非静默放开。
- **改版本号怎么改？** 只允许改 `cmd/loglens/main.go` 的 `const version` 并按本文档流程 commit + tag；不要对产物名或 MCP 版本做手工覆写。