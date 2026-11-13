# LogLens 需求与路线图

> 本文档回答「这个工具该长成什么样」：为什么做、做什么、不做什么、做到哪了。
> 使用方式见 [README](../README.md)，设计与实现见 [DESIGN.md](DESIGN.md)，一轮基于真实排查任务的体验缺陷分析见 [AGENT-EXPERIENCE-REVIEW.md](AGENT-EXPERIENCE-REVIEW.md)。

## 愿景与定位

LogLens 是面向**开发者与 AI Agent** 的单机日志工作台：内置原生 MCP 服务（stdio / Streamable HTTP），让 Claude Code 可以安全、可控地检索、切片、分析日志。

- Slogan：*Logs observed by humans and analyzed by agents.*
- 定位：日志直读、不依赖外部服务，单二进制一条命令跑起来。**主场景：Claude Code 与人在本机，日志文件在远程服务器**——实例部署在日志所在主机，本机经 Streamable HTTP（或 SSH stdio 桥）访问它。
- 澄清：与其它同名 loglens 项目无关。

### 目标

- **时间范围切片查询**是核心能力——按起止时间取一段日志，这是它区别于 `tail` / `grep` 的根本价值。
- 原生支持普通日志与 `.gz` 轮转日志（自动合并 `base.N[.gz]` 轮转组）。
- 内置安全边界，防止 AI 裸读超大日志打满服务器。
- **远程可访问（M2）**：通过 Streamable HTTP 把实例暴露给异机 Claude Code，默认仅监听本机回环 + 可选 token，满足「日志在服务器、Claude 在本地」的主场景。
- 两种消费方式：**TUI** 供人排查，**MCP**（stdio 本地 / Streamable HTTP 远程）供 Claude Code 调用。

### 非目标（建议边界，待确认）

- **不做采集 / 入库 / 索引**：只读所在主机上的文件，进程无状态，退出即还原。
- **不做跨主机聚合**：多主机场景由客户端为每台主机各起一个 LogLens 实例，本地按需连接其中一台的 HTTP 端点。LogLens 每次只看到单机目录，不聚合多主机；「远程访问」≠「跨主机聚合」。
- **不是通用日志平台替代品**：不取代 ELK / Loki 等收集与分析平台。

## 交付范围

### M1 · MVP（已交付）

- MCP 工具 ×3：`list_log_files` / `get_log_slice` / `search_logs`
- 强制安全控制：目录白名单、行数上限、字节上限、超时
- 单二进制、纯 Go，子命令 `mcp`（默认）/ `version`

### M2 · 远程形态 + 分析工具（规划，已定稿 2026-11-11）

主场景是「Claude 在本地、日志在远程」，因此 M2 先交付远程访问形态，再交付**在数据所在地聚合 / 解析**的分析工具（避免把日志行拉回本地再算，省传输与 token）。

- `serve` 子命令（新增，远程形态）：
  - Streamable HTTP，官方 go-sdk 自带 `NewStreamableHTTPHandler`，零新依赖。
  - 默认 bind `127.0.0.1:8787`，可选择 `--token` 要求 `Authorization: Bearer`；TLS 交由 SSH 隧道 / 前置代理；首版不做多用户隔离，白名单仍是进程级。
  - 本地消费姿势：SSH 隧道映射端口后注册 `"type":"http"` 的 remote URL；临时排查也可用 `ssh host "loglens mcp"` 的 stdio 桥（零部署、无常驻）。
- `aggregate_stats`（头号）：按时间桶 / 正则捕获组聚合计数，统计在**服务端**完成、只回传小体积 JSON。复用扫描核心（需先抽 `query` 扫描为回调式）；新增安全面：分组基数内部上限 + `top_n` / `min_count` 过滤，防 agent 参数导致内存 / 返回爆炸。
- `parse_stacktrace`：Java / Go panic / C++ backtrace / FFmpeg 报错解析。**纯文本输入**（`text` 参数，agent 先用切片 / 搜索取到片段再传入），不依赖文件访问；识别不出时返回 `kind=unknown` + 原样文本 + 解析统计，不报错。
- `get_recent_tail`：~~最近 N 条~~ → **移除**。无界 `get_log_slice` 已自动走 tail（`tailMode`）；真空白「跨轮转组 tail」需顺序读大 gz，远程高延迟下体验差，留待明确诉求再单独立项。

### M3 · TUI 与链路聚合（规划，待评审）

- TUI 交互式界面：浏览、检索、高亮、保存查询模板（本地运行，经 Stdio / HTTP 连接本机或远程实例）
- `get_trace_chain`：按 traceId 跨文件聚合链路日志（依赖 M2 基础能力与跨文件检索）

## 非功能约束

- **资源上限**：单次查询强制受限于行数 / 字节 / 超时，见 README「安全上限速查」。
- **优雅降级**：资源耗尽时返回「部分结果 + `truncated` 标记与原因」，而非报错，调用方可自行换参重试。
- **远程鉴权**：HTTP 形态默认仅监听本机回环；跨机访问建议配合 SSH 隧道与 `--token`，白名单仍为进程级、不做多用户隔离。
- **依赖最小化**：仅依赖官方 MCP SDK（纯 Go），编译产物为单一静态二进制。

## 项目元信息

- module 路径：`github.com/sun-crest/loglens`
- 远端仓库：`gh-sun:sun-crest/loglens`（上游跟踪 `origin/master`）
- 版本号与发版流程：`docs/RELEASING.md`