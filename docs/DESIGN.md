# LogLens 设计与实现

> 本文档记录架构分层与关键技术决策。需求与路线图见 [REQUIREMENTS.md](REQUIREMENTS.md)，使用方式见 [README](../README.md)。

## 架构分层

```
cmd/loglens/main.go      入口：子命令 mcp（默认）/ tui / version
├── internal/logreader   文件读取、gz 流式解析、懒加载、轮转组识别、列表
├── internal/parser      时间戳识别（zap/Java/JSON/标准/syslog）
├── internal/query       查询引擎：时间切片、AND/OR 过滤、上下文、资源上限
├── internal/mcp         MCP server（官方 go-sdk）、工具定义、schema
├── internal/security    白名单、路径解析防穿越、限额、超时
└── internal/tui         (占位，M3 里程碑)
```

调用链：`main → mcp(注册工具) → mcp/tools → query + logreader + parser`，
任何文件访问都先经 `security.Resolve` 校验。

## 关键技术决策

1. **单二进制、纯 Go**：`loglens mcp` 零第三方运行时依赖；唯一外部依赖是官方 MCP SDK（纯 Go），编译后仍为单一静态二进制。
2. **gz 流式解压**：用 `compress/gzip` 边读边解，绝不解压到磁盘；大文件懒加载 + `bufio` 分块读取。
3. **防御纵深**：工具层把路径交给 `security.Resolve` 二次校验后才打开；轮转组的**每个兄弟文件**同样过检，防止轮转名被替换成符号链接逃逸。
4. **优雅降级**：资源耗尽时不返回错误，而是返回**部分结果 + `truncated` 标记与原因**，让 Claude 知道停在哪一步、可以自行换参数重试。

## 安全模型

- 所有文件访问经过**白名单校验**（含符号链接解析，防路径穿越 / 软链逃逸）。
- 单次查询强制受限于四个参数（见 README「安全上限速查」）：
  - `LOGLENS_WHITELIST_DIRS`：允许访问的日志目录，白名单以外**禁止访问**
  - `LOGLENS_MAX_LINES`：最大返回行数
  - `LOGLENS_MAX_BYTES`：最大读取字节
  - `LOGLENS_TIMEOUT_MS`：查询超时，超时终止任务并返回已读部分

## 测试策略

- 单元 + 集成测试覆盖 `logreader` / `query` / `mcp` / `parser` / `security`。
- MCP 集成测试通过官方 SDK 客户端走完整握手。
- `testdata/logs/` 提供冒烟夹具：普通日志 + gz 轮转 + Java 格式。
- 竞态检测：`go test -race ./...`。