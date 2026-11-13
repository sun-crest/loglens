# LogLens 查询引擎体验复测（第二轮）：同一任务在 P0/P1 落地后的对照

> 本文档记录同一触发任务在【时间窗 seek / tail+关键词 / per_file 覆盖上下文】等修复落地**之后**的真实操作体验：
> 验证哪些旧能力已真正生效，同时暴露一组**新一层**的问题——目标事件位于文件中部的「最近一次调用」场景仍没有高效原语。
> 相关实现见 [DESIGN.md](DESIGN.md)、[REQUIREMENTS.md](REQUIREMENTS.md)；第一轮缺陷分析见 [AGENT-EXPERIENCE-REVIEW.md](AGENT-EXPERIENCE-REVIEW.md)。

## 结论摘要

> **第一轮的「盲区不可达」已被 seek 与 per_file 覆盖彻底解决；但同一诉求在更一般的形态下——事件发生在文件中部、且需确证其后无更晚命中——仍要 4 轮试探查询才能给出答案。**

第一轮 P0/P1/P2 修复在本轮任务中绝大部分**经受住了真实复测**。新暴露的问题集中在五个点，**全部已随本文档落地**：

- **P0（最高频诉求原语缺失）**：「最近一次调用某接口」仍只能靠猜时间窗 + 多次补搜；`tail+keywords` 仅覆盖文件末尾 MaxBytes 窗口，够不到中部事件。**✅ `tail_backwards`**；
- **P1（返回体积失控）**：单次响应可因关键词过宽 + context 放大 + 超长单行爆掉 MCP token 上限（实测 837 行 / 63 KB 落盘）。**✅ `max_line_chars` / `max_result_chars` + `text_truncated`**；
- **P1（诊断二次判断成本）**：`truncated`/0 命中的下一步引导仍依赖 Agent 手工换算 `per_file` 覆盖字段。**✅ 响应 `summary` 字段**；
- **P2（每行双次时间解析）**：无时间窗 × keywords 路径每行解析两次时间戳，与文档「每行只解析一次」承诺不符。**✅ 复用外层解析**；
- **P2（列表无排序）**：`list_log_files` 不按时间排序，文件名不含日期时无法判断新旧。**✅ `sort: "time"`**。

## 环境与触发任务

- 环境：`loglens` 查询引擎 + loglens-234 MCP，`LOGLENS_MAX_BYTES=10485760`（10 MiB）。
- 目标文件：单日日志 `app_2026-11-12.log`，约 22 MB，覆盖 00:00~23:20（+0800）。
- 排查诉求：「找到最近一次调用 `/api/v1/analysis/tasks/%s/start` 接口的信息，并解析出请求 body」，其中 `%s` 为占位符（task_id）。

## 实际执行流程（4 步拼出答案）

| 步骤 | 操作 | 结果 | 暴露的问题 |
|---|---|---|---|
| 0 | `list_log_files`（全量） | 返回 124 个文件，无排序 | 靠文件名猜最新文件 |
| 1 | `search_logs`（两个 11-12 文件, `-30h`, keywords=`tasks`+`start`, context 3/5） | **837 行 / 63 KB，超 token 上限落盘** | 关键词过宽（`start` 命中 `start_time_unix_ms` 等）+ 上下文放大 → 响应爆炸 |
| 2 | `search_logs`（regex `analysis/tasks/`+`/start"`, 17:00 起） | **byte budget 截断，只扫到 19:21** | 单次预算扫不完整个下午 |
| 3 | `search_logs`（21:00 起） | 空，`complete=true` | 确认尾部无命中 |
| 4 | `search_logs`（19:10~21:00） | 命中 19:22、19:37 | 最终确认 19:37 为最近一次 |

最终答案：`2026-11-12 19:37:09.928+0800`，POST `.../analysis/tasks/2322c92a-2bbc-451a-b337-5e588ebefe0e_1112193700/start`，body 含 2 路相机及场地单应矩阵（详见主会话分析）。

> 关键事实：该接口当天调用分布在 15:21~19:37，即**文件中部**；尾部的 10 MiB 窗口与此无关，必须向前补搜。

## 对照一：已落地修复在本轮的验证结论

| 第一轮修复 | 本轮验证 | 说明 |
|---|---|---|
| **P0-1 时间窗 seek 定位** | ✅ 生效 | 步骤 1 用 `-30h` 就能冲进 22 MB 文件深处并返回命中（而非旧的 0 命中+截断），seek 真正起效 |
| **P1-1 per_file 扫描覆盖** | ✅ 生效 | 步骤 2 截断时 `per_file` 明确给出 `bytes_read/total`、`seek_offset`、`scanned_from/to`、`complete=false`，据此判断「答案不在尾部，须向前补搜」 |
| **P1-3 per_file_budget** | ✅ 存在 | 多文件独立预算可用（本次单文件场景未触发） |
| **P0-2 tail + 关键词** | ⚠️ 部分适用 | 覆盖文件末尾 MaxBytes 窗口；本次目标事件位于**中部**，窗口够不到，需换时间窗+seek |
| **P1-2 tail 行号语义标注** | ✅ 成立 | 步骤 2/4 返回的 `line` 基数不同（788 vs 3761/9989），但 `seek_offset` 已显式标注，本次不是障碍 |

**结论**：第一轮的核心场景（文件后部一定范围内的目标）已被现有能力覆盖；但「事件在中部、须确证其前后时点」的**通用最近一次**诉求仍然未闭合。

## 新暴露问题详情

### 3.1 P0 · 「最新一次命中」没有高效原语

**现象**：目标事件位于文件中部时，`tail+keywords` 覆盖不到；`search_logs + start_time` 又必须猜时间窗，且一步预算扫不完大文件（步骤 2 仅 17:00→19:21 即耗尽 10 MiB）。结果：4 轮试探（一次猜宽 → 被截断 → 分两段补搜）才确定最近一次。

**与 M2 `aggregate_stats` 的关系**：`aggregate_stats` 解决「统计（几次/分布在何时）」；但「精确到某次调用的时间戳 + 取回 body」仍须靠近定位，二者互补而非替代。

**建议**：
- `search_logs` 增加 `tail_backwards`（从文件末尾分块向前扫描，预算可多轮往复，返回**最新 N 条命中**及上下文）——把「最近一次调用 X」收敛为单次原语；
- 或退而求其次：tail 查询 0 命中时响应式提示「tail 窗口覆盖 <起止时间>，目标可能更早，建议向前 seek」。

**落地**（2026-11-12，随本轮实现）：

- `Options.TailBackwards` + `runTail` 反向分支 + `reverseTailEntries`（[internal/query/engine.go](../internal/query/engine.go)）：普通文件从尾部向前按 `256 KiB` 分块扫描，跨块行用 carry 拼接；读到 `LimitLines` 条命中即早停（新在末尾的时序还原）；字节预算从尾部消耗，预算耗尽同样 `truncated + per_file` 覆盖提示。
- `search_logs` / `get_log_slice` 均支持 `tail_backwards: true`（[internal/mcp/tools.go](../internal/mcp/tools.go)）。`search_logs` 的 `FullScan` 不再压制反向尾搜索（`tailMode()` 对 `TailBackwards` 放行）。
- 覆盖语义：反向扫描记录的是**文件顺序**的首末时间戳（`scanned_from` = 头部侧，`scanned_to` = 尾部侧），与扫描方向解耦，Agent 无需换算。
- 测试：`TestTailBackwardsFindsMidFileMatches` / `TestTailBackwardsUnderBudget` / `TestTailBackwardsNoNewlineAtEOF` / `TestTailBackwardsIgnoresFullScan` / `TestTailBackwardsReportsCoverage` / MCP 层 `TestSearchLogsTailBackwards`。真实日志回归：7.6 MiB 文件中部（第 15001/20001 行）的 `/start` 调用一次查询命中。

### 3.2 P1 · 单次响应可爆 token（837 行 / 63 KB 落盘）

**成因**：

- 关键词子串 AND 语义天然过宽：`start` 同时命中 `start_time_unix_ms` / `started_at` 等，本意「URL 末尾的 /start」没有子串表达，被迫上正则（`/start"`）；
- `context_before/after` 会把命中行的前后行都收进来，放大返回；
- 单行日志可含大 JSON body（实测 `bodyLen` 达 110 KB / 129 KB），`LimitLines` 只管**行数**、没有**字符级上限**，一条长命中即可撑爆响应。

**落地**（2026-11-12，随本轮实现）：

- **`max_line_chars`**：任意单行返回文本超阈值时从 rune 边界截断，条目标记 `text_truncated`——一条 110 KB 的 JSON 行无法单独撑爆响应；
- **`max_result_chars`**：聚合返回文本字符预算，超限即丢弃后续条目并置 `truncated`（`reason` 区分 `max_result_chars` 与 line limit）；
- 两组上限在 `scanReader`（含 context 行）、`runTail`、`reverseTailEntries` 三条路径统一生效（`appendEntry` / `finalizeEntries`）。
- 测试：`TestMaxLineCharsTruncation` / `TestMaxResultCharsTruncation`。

### 3.3 P1 · truncated / 0 命中的「下一步指引」依赖 Agent 手工解析 per_file

**现象**：覆盖信息其实**都在**（`scanned_from/to`、`bytes_read/total`、`complete`），但要据此决策「换窗口 / 加大预算 / 向前扩」仍须 Agent 逐字段换算。

**落地**（2026-11-12，随本轮实现）：

- `Result` 新增 `summary` 字段：truncated / 0 命中时填充一行引导，如「byte budget used up; scanning stopped at <时间> — narrow the time window or raise LOGLENS_MAX_BYTES」「no matches in the scanned range (... to ...); try other keywords...」，把 per_file 的解释成本从 Agent 端收回。
- 测试：`TestSummaryForEmptyAndTruncated`。

### 3.4 P2 · 无时间窗 × keywords 路径每行解析两次时间戳

**代码定位**（[internal/query/engine.go](../internal/query/engine.go)）：

- [engine.go:292](../internal/query/engine.go#L292)：`tt, hasTime := parser.ExtractLineTime(l.Raw, now)` —— 无论有无 bounds 都先解析一次；
- [engine.go:318](../internal/query/engine.go#L318)：`} else if tt, ok := parser.ExtractLineTime(l.Raw, now); ok` —— `HasBounds=false`（即 `search_logs` 无时间窗全文搜索）时**再解析一次**。

带 bounds 分支只解析一次；无 bounds 的全文搜索每行两次。**与 AGENT-EXPERIENCE-REVIEW.md 声称的「P1-1 落地时每行只解析一次、顺带修复重复解析开销」不符**——该路径落了空。大文件全文搜索时有可量化浪费（`ExtractLineTime` 顺序尝试 13 种布局）。

**落地**（2026-11-12，随本轮实现）：无 bounds 分支改写为复用外层 `hasTime` / `tt`（[engine.go](../internal/query/engine.go) 的 `scanReader`），删除 `else if` 内的 `ExtractLineTime` 二次调用——每行在整个扫描中只解析一次时间戳。（该修复无独立测试断言——解析次数是性能细节；由既有全套测试与 race 验证行为不变。）

**建议**：无 bounds 分支复用外层解析结果（`tt`/`hasTime`），去掉 `else if` 内的二次调用。

### 3.5 P2 · `list_log_files` 返回无排序

**现象**：124 个文件靠文件名猜最新；若文件名不含日期（`access.log` 类轮转组），无法判断新旧。

**落地**（2026-11-12，随本轮实现）：`ListOptions.SortByTime` + `list_log_files` 新增 `sort: "time"` 参数（按 `mod_time` 降序、最新在前），默认仍按路径排序保持既有行为。测试：`TestListFilesSortByTimeNewestFirst`（logreader）、`TestListLogFilesSortTime`（MCP）。

### 3.6 记录（非缺陷）· tail 行号基数不一致

步骤 2 `line=788` vs 步骤 4 `line=3761/9989`，基数因 `seek_offset` 而异；`seek_offset` 已显式标注（P1-2），本轮不构成障碍，仅记录在案。

## 优化建议（按优先级）

| 优先级 | 优化项 | 做法 | 解决的体验问题 |
|---|---|---|---|
| **P0** | 反向查最新命中 | `search_logs` 增 `tail_backwards`：从最新文件末尾分块反向扫描、预算内返回最新 N 条命中；0 命中时提示覆盖窗口 | 4 轮试探 → 1 轮 ✅ 已落地 |
| **P1** | 响应字符级上限 / 超长行截断 | `max_line_chars` 单行截断 + `text_truncated`；`max_result_chars` 聚合上限 | 63 KB 落盘式响应崩塌 ✅ 已落地 |
| **P1** | 截断/0 命中诊断摘要 | 响应顶层 `summary` + 换参建议 | 收窄 Agent 二次判断成本 ✅ 已落地 |
| **P2** | 修复双重时间解析 | 复用外层 `ExtractLineTime` 结果，删除 [engine.go:318](../internal/query/engine.go#L318) 二次调用 | 兑现「每行一次」承诺，大文件提速 ✅ 已落地 |
| **P2** | `list_log_files` 按时间排序 | `sort: "time"`（`mod_time` 降序、最新在前），默认仍按路径 | 枚举—选最新文件的常见流程 ✅ 已落地 |

> 以上均不引入新依赖、不动架构，复用现有 `seek` / `Iterate` / `per_file` 基础设施；与 M2 的 `aggregate_stats` 正交，且有铺垫关系。

## 验收建议（均已通过）

1. **P0 反向**：对 22 MB `app_2026-11-12.log`，`tail_backwards + keywords(analysis/tasks/ + /start)` 一次返回包含 19:37 的最近 N 条命中，无需预知时间窗；**✅ 已在 7.6 MiB 真实形态文件回归通过**（文件中部 15001/20001 行的调用一次命中）；
2. **P1 体积**：构造含 100 KB+ 单行的命中，单次响应不落盘（`text_truncated` 标记）；聚合超限置 `reason: max_result_chars`；**✅ `TestMaxLineCharsTruncation` / `TestMaxResultCharsTruncation`**；
3. **P1 诊断**：任何 `truncated` / tail 0 命中响应附带 `summary` 换参建议；**✅ `TestSummaryForEmptyAndTruncated`**；
4. **P2 解析**：`search_logs` 无时间窗 + keywords 时，引擎扫描期间每行只调用一次 `ExtractLineTime`；**✅ 代码修复**（行为由既有测试保持）；
5. **P2 列表**：`list_log_files sort: "time"` 输出按修改时间降序，最新文件居首；**✅ `TestListFilesSortByTimeNewestFirst` / `TestListLogFilesSortTime`**。