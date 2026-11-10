# LogLens 需求与路线图

> 本文档回答「这个工具该长成什么样」：为什么做、做什么、不做什么、做到哪了。
> 使用方式见 [README](../README.md)，设计与实现见 [DESIGN.md](DESIGN.md)。

## 愿景与定位

LogLens 是面向**开发者与 AI Agent** 的本地日志工作台：内置原生 MCP 服务，让 Claude Code 可以安全、可控地检索、切片、分析日志。

- Slogan：*Logs observed by humans and analyzed by agents.*
- 定位：本地文件直读，不依赖外部服务，一条命令在单台主机上跑起来。
- 澄清：与其它同名 loglens 项目无关。

### 目标

- **时间范围切片查询**是核心能力——按起止时间取一段日志，这是它区别于 `tail` / `grep` 的根本价值。
- 原生支持普通日志与 `.gz` 轮转日志（自动合并 `base.N[.gz]` 轮转组）。
- 内置安全边界，防止 AI 裸读超大日志打满服务器。
- 两种消费方式：**TUI** 供人排查，**MCP stdio** 供 Claude Code 调用。

### 非目标（建议边界，待确认）

- **不做采集 / 入库 / 索引**：只读本地文件，进程无状态，退出即还原。
- **不做跨主机聚合**：多主机场景由客户端为每台主机各起一个 LogLens 实例编排。
- **不是通用日志平台替代品**：不取代 ELK / Loki 等收集与分析平台。

## 交付范围

### M1 · MVP（已交付）

- MCP 工具 ×3：`list_log_files` / `get_log_slice` / `search_logs`
- 强制安全控制：目录白名单、行数上限、字节上限、超时
- 单二进制、纯 Go，子命令 `mcp`（默认）/ `version`

### M2 · 补齐 MCP 工具（规划，待评审）

- `get_recent_tail`：最近 N 条
- `parse_stacktrace`：Java / Go panic / C++ backtrace / FFmpeg 报错解析
- `aggregate_stats`：按字段 / 正则聚合统计

### M3 · TUI 与链路聚合（规划，待评审）

- TUI 交互式界面：浏览、检索、高亮、保存查询模板
- `get_trace_chain`：按 traceId 跨文件聚合链路日志（依赖 M2 基础能力）

## 非功能约束

- **资源上限**：单次查询强制受限于行数 / 字节 / 超时，见 README「安全上限速查」。
- **优雅降级**：资源耗尽时返回「部分结果 + `truncated` 标记与原因」，而非报错，调用方可自行换参重试。
- **依赖最小化**：仅依赖官方 MCP SDK（纯 Go），编译产物为单一静态二进制。

## 项目元信息

- module 路径：`github.com/sun-crest/loglens`
- 远端仓库：`gh-sun:sun-crest/loglens`（上游跟踪 `origin/master`）