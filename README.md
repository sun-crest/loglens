# LogLens

> A local log viewing & analysis workspace, native MCP server for Claude Code.
> Not related to other projects named loglens. This one is built for human engineers and AI agents, with time-slice query, gz rotated log support, TUI and safe bounded query for MCP.

LogLens 是面向开发者与 AI Agent 的本地日志工作台，内置原生 MCP 服务，让 Claude Code 可以安全、可控地检索、切片、分析日志。

✅ 核心特性
- 时间范围切片查询（核心能力，区别于 tail / grep）
- 原生支持普通日志、`.gz` 轮转日志（自动合并 `base.N[.gz]` 轮转组）
- 内置安全限制：行数上限、字节上限、超时、日志目录白名单，防止 AI 裸读超大日志打满服务器
- 两种运行模式：MCP stdio 给 Claude Code 调用；TUI 人工排查（规划中，见路线图）

Slogan：*Logs observed by humans and analyzed by agents.*

## 快速开始

### 构建与测试

```bash
go build ./cmd/loglens          # 生成二进制 loglens
go test ./...                   # 单元 + 集成测试（内部通过官方 SDK 客户端走完整 MCP 握手）
go test -race ./...             # 竞态检测
```

### MCP stdio（给 Claude Code）

```bash
LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs \
LOGLENS_MAX_LINES=2000 \
LOGLENS_MAX_BYTES=10485760 \
LOGLENS_TIMEOUT_MS=15000 \
  loglens mcp
```

### TUI

`loglens tui` 当前尚未实现，为后续里程碑（见 [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md)）。

## Claude Code MCP 配置

在 Claude Code 中注册 `loglens` 服务器：

```json
{
  "mcpServers": {
    "loglens": {
      "command": "loglens",
      "args": ["mcp"],
      "env": {
        "LOGLENS_WHITELIST_DIRS": "/var/log,/opt/service/logs",
        "LOGLENS_MAX_LINES": "2000",
        "LOGLENS_MAX_BYTES": "10485760",
        "LOGLENS_TIMEOUT_MS": "15000"
      }
    }
  }
}
```

## 安全上限速查

所有文件访问都经过白名单校验（含符号链接解析，防路径穿越 / 软链逃逸）；每次查询强制受限于：

- `LOGLENS_WHITELIST_DIRS`：允许访问的日志目录，逗号分隔，**禁止访问白名单以外路径**
- `LOGLENS_MAX_LINES`：单次查询最大返回行数
- `LOGLENS_MAX_BYTES`：单次读取最大字节上限
- `LOGLENS_TIMEOUT_MS`：单次查询超时，超时直接终止任务并返回已读取的部分

超过任一上限时不报错，而是返回**部分结果 + `truncated` 标记与原因**。

## 当前 MCP 工具

- `list_log_files`：列出白名单目录下日志文件，支持 glob、递归、识别 gz 轮转文件
- `get_log_slice`：按【起止时间 + 关键词 + 上下文行数】切片日志（核心，自动合并 `base.N[.gz]` 轮转组）
- `search_logs`：关键词 / 正则检索日志，支持排除关键词、忽略大小写、跨文件

规划中的新工具见 [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md)。

## 日志时间戳语义（重要）

- 带时区/`Z` 的时间戳（zap、RFC3339、JSON `@timestamp`）按字面时区解析。
- **不带时区的时间戳按服务器本地时区解析**。因此查询边界请与日志文件本身保持一致：日志是 UTC（`Z` 结尾）就在 `start_time`/`end_time` 用带 `Z` 的 RFC3339；日志是本地时间（如 Java `2026-11-10 12:10:03,123`）就用 `2026-11-10 12:10:03` 这类写法，或直接引用日志里出现的时间串。
- `start_time` 支持 `-2h` / `-30m` / `-1d` 相对值、`now`、裸日期、纯时间（今天）。

## 目录结构

```
loglens/
├── cmd/loglens/main.go       # 入口，子命令：mcp（默认）/ tui / version
├── internal/
│   ├── logreader/            # 文件读取、gz 流式解析、懒加载、轮转组识别、列表
│   ├── parser/               # 时间戳识别（zap/Java/JSON/标准/syslog）
│   ├── query/                # 查询引擎：时间切片、AND/OR 过滤、上下文、资源上限
│   ├── mcp/                  # MCP server（官方 go-sdk）、工具定义、schema
│   ├── security/             # 白名单、路径解析防穿越、限额、超时
│   └── tui/                  # (占位)
├── docs/
│   ├── REQUIREMENTS.md       # 需求与路线图（目标、非目标、里程碑）
│   └── DESIGN.md             # 设计与实现（架构分层、关键技术决策）
├── testdata/logs/            # 冒烟测试夹具（普通 + gz 轮转 + Java 格式）
├── go.mod / go.sum
└── README.md
```