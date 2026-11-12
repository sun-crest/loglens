# LogLens 查询引擎使用体验缺陷分析

> 本文档记录一次真实的 Agent 排查任务中发现的查询引擎缺陷：现象、根因（含代码定位）、优化建议与验收标准。
> 触发任务：在 42MB 单日日志中定位「最近一次调用 `/api/v1/analysis/tasks/%s/start` 接口并解析请求 body」。
> 相关实现见 [DESIGN.md](DESIGN.md)、[REQUIREMENTS.md](REQUIREMENTS.md)。

## 结论摘要

本次排查暴露出现有查询引擎的三级矛盾：

> **字节预算的本意是「安全」，当前实现却让它同时决定了「能否到达目标区间」。**

- P0（核心能力缺失）：时间窗查询对位于文件后部的窗口**不可达**——42MB 日志只能覆盖开头约 3 小时 / 10 MiB；
- P0（高频诉求缺失）：没有「从尾部反向搜关键词」的能力——「最近一次调用某接口」无从下手；
- P1（可诊断性不足）：`truncated` 结果不告知扫描覆盖了哪里，Agent 无法区分「真没有」与「没扫到」；
- P1（行号语义漂移）：tail 返回的 `line` 是 seek 偏移后的相对行号，与普通搜索的真实行号不一致，二次定位不可靠。

另有 4 项 P2 级次要问题（多文件预算不均、逐行时间解析开销、列表上限耦合、截断原因粒度），均已实现（见 P0/P1/P2 落地记录）。

## P0 优化落地记录（2026-11-12）

两项 P0 已实现并通过真实日志回归验证：

### P0-1 · 时间窗 seek 定位（已实现）

- `locateStartOffset`：对普通（非 gz）文件二分探测行首时间戳，定位 `Start` 对应字节偏移，回退 32 KiB 后从该处起扫；
- 预算从定位点重新起算，读取不再被文件头无关行占用；
- 扫过 `End`（且无 context_after 待补）即提前终止（`errWindowPassed`），避免预算浪费在窗口之后；
- **真实日志回归**：42 MiB `app_2026-11-12.log`，查 `09:49~11:05` 窗口 + `tasks` 关键词 → `truncated=false`，扫描 8.1 MB，一次返回 25 条（原来 0 命中 + 截断）。

### P0-2 · tail + 关键词（已实现）

- `tailMode` 允许 keywords/exclude（无 bounds、无 context）；`search_logs` 通过 `Options.FullScan` 保持「无时间窗 = 整文件扫描」的旧契约，`get_log_slice` 无时间窗 = 尾部窗口内过滤；
- `runTail` 在窗口内应用 matcher，返回**最近的** N 条匹配；
- 边界提示：tail 窗口是文件末尾 `MaxBytes` 内的数据，若目标事件在更早位置，应改用时间窗 + seek（P0-1）而非 tail。

### 相关代码

- [internal/query/engine.go](../internal/query/engine.go)：`locateStartOffset`/`probeTimeAt`/`errWindowPassed`/`tailMode`/`FullScan`/`scanReader`
- [internal/mcp/tools.go](../internal/mcp/tools.go)：`getLogSliceDesc`/`searchLogsDesc` 更新，`search_logs` 置 `FullScan`
- 测试：[engine_test.go](../internal/query/engine_test.go) `TestSeekLocatesStart`/`TestSeekHandlesEarlyStart`/`TestTailWithKeywords`

> 未实现（后续迭代）：P1 · ~~结果带扫描覆盖上下文~~ ✅、P1 · ~~tail 行号修正~~ ✅、P1 · ~~多文件预算按文件分配~~ ✅。

## P1 优化落地记录（2026-11-12）

三项 P1 已实现：

### P1-1 · 结果带扫描覆盖上下文（已实现）

`Result` 新增 `per_file` 数组（`FileScan`）：每个被扫描文件记录 `bytes_read` / `bytes_total`（可计算覆盖比例）、`seek_offset`（扫描起点字节偏移）、`scanned_from` / `scanned_to`（首末行时间戳）、`complete`（是否读到 EOF）。truncated 结果据此告知「扫到哪、还差多少」，Agent 可判断是换窗口还是加预算。扫描期间对每行只解析一次时间戳（顺带修复了 P2 的重复解析开销）。

### P1-2 · tail 行号语义（已实现）

seek / tail 查询中 `entry.line` 是相对扫描起点的行号，`FileScan.seek_offset` 给出该起点，Agent 可据此还原绝对位置；`seek_offset = 0`（整体读）时 `line` 即真实行号。MCP 工具描述明确了该语义。绝对行号需要全扫 seek 起点前内容，与 seek 的初衷冲突，故采用「显式标注」方案（符合验收条款 b）。

### P1-3 · 多文件预算按文件分配（已实现）

新增 `per_file_budget` 选项（`search_logs` / `get_log_slice` 均可传）：每个文件独立享有一份 `LOGLENS_MAX_BYTES`，不再共享一个全局预算。同时修正 `Run` 循环：per-file 模式下单个文件截断不会 cascade 终止后续文件（`TestPerFileBudgetTruncatedOneStillScansOthers` 验证）。默认关闭，保持既有共享预算行为不变。

> 剩余：~~P2 · `ExtractLineTime` 短路~~ ✅、~~P2 · `list_log_files` 独立上限~~ ✅、P2 · 截断原因细化 ✅。

## P2 优化落地记录（2026-11-12）

- `ExtractLineTime` 短路：随 P1 落地时顺带完成（`scanReader` 每行只解析一次，fs 元数据与 entry 时间复用同一结果）。
- `list_log_files` 独立上限：新增 `security.Limits.MaxFiles` + `LOGLENS_MAX_FILES` 环境变量（默认 5000），与 `MaxLines` 解耦；响应新增 `truncated` 字段，按上限截断时显式告知。
- 截断原因细粒度：`reason` 已区分 `line limit reached` 与 `byte budget exceeded`，工具描述补充对应换参引导（缩窗口 vs 调 limit / MAX_BYTES）；README「安全上限速查」同步说明 `per_file` 覆盖与 `LOGLENS_MAX_FILES`。

## 触发场景（复现步骤)

环境：`loglens serve` 部署在远程主机，`LOGLENS_MAX_BYTES=10485760`（10 MiB）。
目标文件：单日日志 `app_2026-11-12.log`，约 42 MB，覆盖 00:00~14:00。
排查诉求：「找到最近一次调用 `/api/v1/analysis/tasks/%s/start`，解析请求 body」，其中 `%s` 为占位符。

实际执行中观测到的现象：

1. 请求 `13:40~14:00`、`10:00~11:00`、`00:00~07:00` 等时间窗（带关键词或不带）→ 全部 `total_matched: 0` + `byte budget exceeded` 截断；
2. 但这些窗口**确有日志**（无时间窗 tail 可看到 13:46 的 DBCost 记录）；
3. 对 10+ 组关键词做了 `search_logs`，永远是 `0 matched + truncated`，无法判断调用是否存在于未扫区域；
4. 多文件 `search_logs` 传 3 个文件时，只扫了第一个文件的 10 MiB，其余文件完全不读。

### 关键实测证据（本机副本验证）

将服务器日志副本拷到本机后直接检索，发现**该接口当天被调用了多次**，且**全部落在 10 MiB 字节预算的盲区内**：

| 项目 | 数值 |
|---|---|
| 文件 | 206,578 行 / 42 MiB |
| 前 10 MiB 可达 | 行 1~51,014（00:00~04:46） |
| 后 10 MiB 可达 | 行 157,361~206,578 |
| 中部盲区 | 行 51,015~157,360（约 04:46~11:30 时段） |
| `/tasks/{id}/start` 调用 | **8 处**，行 113,323~145,028（09:50~11:00），**全部位于盲区** |

也就是说，目标事件恰好是「字节预算从文件头扫描时永远够不到」的区间。若实现支持 seek 定位，这 8 处调用可在一次查询内直接返回；当前实现下无论怎么调参数（时间窗、关键词、regex）都必然 `0 matched`。

- 顺带确认：调用确有携带 body 的 `HTTP请求发起` 记录（POST 到 `172.28.25.170:18110`），正是目标接口的可解析请求体。

## 现象与根因

### 1. P0 · 时间窗查询无法到达文件后部（核心能力缺失）

**现象**：凡 `start_time` 落在「文件开头 10 MiB 之外」的时间窗，全部 0 命中 + 字节预算截断。

**根因**（[engine.go](../internal/query/engine.go)）：

- 带时间窗/关键词的路由进入 `runFile`，从**文件头**开始 `logreader.Iterate` 顺序扫描（[engine.go:95-102](../internal/query/engine.go#L95-L102)、[engine.go:130-132](../internal/query/engine.go#L130-L132)）；
- 字节预算 `MaxBytes - ScannedBytes` 耗尽即 `ErrBudgetExceeded`（[reader.go:79-81](../internal/logreader/reader.go#L79-L81)）；
- 时间窗只作为逐行过滤器 `tt.Before(start) || tt.After(end)`（[engine.go:140-142](../internal/query/engine.go#L140-L142)），**不会 seek 到 `Start` 附近**，所以 `Start` 之前的全部无关行照常消耗字节预算。

**影响**：对 42 MB / 10 MiB 预算的折算出,时间窗仅前约 3 小时可达。这与产品定位「时间范围切片查询是核心能力」直接冲突——用户只要 13:40 的几分钟日志,实现却必须读完整个文件头才可能到达。

### 2. P0 · 没有「尾部反向搜索关键词」能力（高频诉求缺失）

**现象**：「最近一次调用某接口」无从下手。

**根因**：

- 纯 tail（无时间窗、无关键词、无上下文）只能取**最后一个文件**的倒数 N 行（[engine.go:236](../internal/query/engine.go#L236)、[engine.go:58-61](../internal/query/engine.go#L58-L61)），不带关键词过滤；
- 只要加了 `keywords`，立刻进入 `runFile` 从头扫描，重新掉进问题 1 的坑。

**影响**：对 Agent 而言「最近一次」是最高频诉求之一。当前要么看最近 2000 行（可能根本没有目标），要么从头扫 10 MiB（到不了事件发生处）。

### 3. P1 · `truncated` 不告知「扫到哪了」（可诊断性缺口）

**现象**：对 42 MB 文件反复搜索,均为 `0 matched + truncated`,无法判断「该时段确实无此调用」还是「调用位于未扫区域」。

**根因**：`Result` 仅含 `scanned_bytes` / `truncated` / `reason`（[engine.go:30-38](../internal/query/engine.go#L30-L38)），没有扫描覆盖区间。而 `MaxBytes` 是服务器配置,Agent 拿不到其数值,也无法换算成「覆盖到哪个时间点」。README/REQUIREMENTS 承诺的「优雅降级：部分结果 + truncated」做到了,但缺少足以驱动「换参重试」的上下文信息。

### 4. P1 · `search_logs` 多文件预算全局共享,首个大文件堵死其余

**现象**：一次传 3 个文件,只扫第一个 11-12（42 MB）的 10 MiB 后截断,后两个文件完全不读。

**根因**：

- 预算跨文件累计：`budget := opts.Limits.MaxBytes - res.ScannedBytes`（[engine.go:124](../internal/query/engine.go#L124)）；
- 循环内 `if res.Truncated { break }` 直接终止后续文件（[engine.go:96-98](../internal/query/engine.go#L96-L98)）。

**影响**：多文件场景没有 per-file 预算,一个超大文件会系统性屏蔽其余文件。

### 5. P2 · 次要观察

| 观察 | 代码位置 | 说明 |
|---|---|---|
| 逐行时间解析开销 | [time.go:17-31](../internal/parser/time.go#L17-L31) | `ExtractLineTime` 每行顺序尝试 13 种 layout;时间窗查询对每行都跑一遍,是大文件全量固定开销 |
| `list_log_files` 上限耦合 | [tools.go:110](../internal/mcp/tools.go#L110) | `MaxFiles = MaxLines*2`,文件列表数量被「返回行数」绑定,大目录被静默截断 |
| 截断原因粒度不足 | [engine.go:189-192](../internal/query/engine.go#L189-L192) | 只区分字节预算/行数/超时;冗长 JSON 行下字节预算先触顶,Agent 不知道该「缩窗口」还是「调 limit」 |

### 6. P1 · tail 返回的 `line` 不是文件真实行号

**现象**：远程 tail 返回「13:46 的 DBCost 在 `line 49160`,13:39 在 `line 49736`」；本机副本里同一批内容真实行号是 `201031` / `197588`，偏差约 4 倍。Agent 若按返回的 `line` 对齐其它工具或人工查看文件,会完全指向错误位置。

**根因**（[engine.go:265-278](../internal/query/engine.go#L265-L278)）：`runTail` 先 `f.Seek(size - MaxBytes)` 到尾部附近,再 `Iterate`;而 `Iterate` 的 `Line.No` 是从 **读取起点重新计数**（[reader.go:71-107](../internal/logreader/reader.go#L71-L107)，`nLines` 从 1 开始）。因此普通搜索（从头扫）返回的行号是文件真实行号，tail 模式返回的是 **seek 偏移后的相对行号**——两者语义不一致。

**影响**：行号字段是查询结果里唯一可「二次定位」的锚点，语义漂移会让 Agent 的精确定位不可靠；`list_log_files` 也不提供行级定位入口，难以交叉验证。

## 优化建议（按优先级)

| 优先级 | 优化项 | 做法 | 解决的体验问题 |
|---|---|---|---|
| **P0** | 时间窗查询 seek 定位 | ✅ 已实现（见「P0 优化落地记录」）:二分定位 `Start` 偏移 + 扫过 `End` 提前终止 | 问题 1,本次 8 处调用全部命中盲区的直接解药 |
| **P0** | 尾部反向搜索 | ✅ 已实现:`tailMode` 扩展为「tail + 关键词」返回最近的 N 条命中;`search_logs` 走 `FullScan` 保持整文件搜索契约 | 问题 2 |
| **P1** | 结果带扫描覆盖上下文 | ✅ 已实现:`Result.per_file[]` 含 `bytes_read/total`、`seek_offset`、`scanned_from/to`、`complete` | 问题 3 |
| **P1** | 多文件预算按文件分配 | ✅ 已实现:`per_file_budget` 选项,每文件独立 MaxBytes;per-file 截断不再 cascade 终止其余文件 | 问题 4 |
| **P1** | tail 行号修正 | ✅ 已实现:`FileScan.seek_offset` 显式标注 line 的相对偏移,`seek_offset=0` 时 line 即真实行号 | 问题 6,二次定位锚点漂移 |
| **P2** | `ExtractLineTime` 短路 | 首字节命中 `\d` `/` `[` `{` 等判型后再进 layout 循环,降低高频轮询日志解析成本 | 性能 |
| **P2** | `list_log_files` 独立上限 | ✅ 已实现:`LOGLENS_MAX_FILES` 环境变量(默认 5000)与 `MaxLines` 解耦,响应含 `truncated` 标志 | 列表可靠性 |
| **P2** | 截断原因细粒度化 | ✅ 已实现:`reason` 区分行数/字节预算触顶,工具描述带换参引导 | 诊断 |

## 验收建议

1. **P0-seek**：对 42 MB 测试文件,查询 `start_time = size 中后部` 的时间窗应直接返回窗口内命中,且 `scanned_bytes` 显著小于文件头全量（≈目标偏移量）,不触发截断。**可用本次实测回归**：`app_2026-11-12.log` 中 `10:00~11:00` / `tasks/{id}/start` 一次查询应返回全部 8 处调用;
2. **P0-reverse**：对已知「发生时间不确定但较新」的事件,`tail + keywords` 组合应一次返回最近命中,无需用户推算时间窗;
3. **P1-上下文**：任何 `truncated` 结果应附「已覆盖到某时间点」信息,使 `0 matched + truncated` 与「确实无命中」可区分;
4. **P1-多文件**：`search_logs` 传入大小悬殊的文件组,每个文件至少获得一份独立扫描机会;
5. **P1-tail 行号**：`line` 语义与普通搜索一致（文件真实行号）或显式标注相对偏移,使行号可跨工具使用。