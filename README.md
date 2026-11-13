# LogLens

> A local log viewing & analysis workspace, native MCP server for Claude Code.
> Not related to other projects named loglens. This one is built for human engineers and AI agents, with time-slice query, gz rotated log support, TUI and safe bounded query for MCP.

LogLens 是面向开发者与 AI Agent 的日志工作台，内置原生 MCP 服务。**主场景：Claude Code 在本机、日志在远程服务器**——把 LogLens 实例部署到日志所在主机，本机 Claude 就可以像查本地文件一样，安全地**搜索、切片、解析远程日志**。

✅ 核心特性
- **本地 Claude · 远程日志（主场景）**：实例部署在日志所在主机，搜索 / 解析在服务器上完成、只回传结果，本机 Claude 经 SSH 隧道 / HTTP 访问（详见下方「远程部署」）
- 时间范围切片查询（核心能力，区别于 tail / grep）
- 原生支持普通日志、`.gz` 轮转日志（自动合并 `base.N[.gz]` 轮转组）
- 内置安全限制：行数上限、字节上限、超时、日志目录白名单，防止 AI 裸读超大日志打满服务器
- 两种消费方式：MCP（stdio 本地 / Streamable HTTP 远程）给 Claude Code 调用；TUI 人工排查（规划中，见路线图）

Slogan：*Logs observed by humans and analyzed by agents.*

## 快速开始

### 构建与测试

```bash
CGO_ENABLED=0 go build ./cmd/loglens   # 静态编译:产物不依赖系统 glibc,可拷到任意 Linux(含旧版发行版)
go test ./...                   # 单元 + 集成测试（内部通过官方 SDK 客户端走完整 MCP 握手，stdio 与 HTTP 两套传输）
go test -race ./...             # 竞态检测

# serve 快速冒烟：起一个需要 token 的本地 MCP HTTP 端点
LOGLENS_WHITELIST_DIRS=/tmp/logs loglens serve --addr 127.0.0.1:8787 --token dev
curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer dev' \
  -H 'Content-Type: application/json' \
  -X POST http://127.0.0.1:8787/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

### MCP stdio（本地）

```bash
LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs \
LOGLENS_MAX_LINES=2000 \
LOGLENS_MAX_BYTES=10485760 \
LOGLENS_TIMEOUT_MS=15000 \
  loglens mcp
```

### TUI

`loglens tui` 当前尚未实现，为后续里程碑（见 [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md)）。

## 远程部署（日志在服务器、Claude 在本地）

主场景：Claude Code 在本地开发机，日志文件在远程服务器。做法：在远程主机上起常驻 `serve`（Streamable HTTP，M2 已实现），本机通过 HTTP 或 SSH 桥访问它。

### 在远程主机启动 serve

```bash
LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs \
LOGLENS_MAX_LINES=2000 LOGLENS_MAX_BYTES=10485760 LOGLENS_TIMEOUT_MS=15000 \
  loglens serve --addr 127.0.0.1:8787 --token your-secret
```

- 默认只监听本机回环，**不暴露到网络**；跨机访问请走 SSH 隧道（见下）。
- `--token`（可选）：要求每个请求带 `Authorization: Bearer <token>`；即使走 SSH 隧道也建议开启。
- TLS 不内置：公网直连请用 nginx 等前置代理终结 TLS。

> **局域网直连（可选）**：同一局域网内想不建隧道直连，把 `--addr` 绑到内网 IP 并用强随机 token。注意这是**明文 HTTP**，信任边界 =「局域网内的可信设备」；要加密仍请走 SSH 隧道或前置 TLS。完整端到端流程见下方「方式 3 · 局域网直连」。绑定建议用具体内网 IP 而非 `0.0.0.0`——后者会把服务暴露到本机所有网卡（含 VPN）。

### 本地连接

**方式 1 · SSH 隧道 + HTTP（推荐，常驻）**：先把远程端口映射到本机（保持前台或后台运行）：

```bash
ssh -N -L 8787:127.0.0.1:8787 user@your-server
```

然后在 Claude Code 注册远程服务器：

```json
{
  "mcpServers": {
    "loglens-remote": {
      "type": "http",
      "url": "http://127.0.0.1:8787/mcp",
      "headers": {
        "Authorization": "Bearer your-secret"
      }
    }
  }
}
```

**方式 2 · stdio 桥（零部署、无常驻）**：让本地进程就是一次 SSH 调用，stdin/stdout 桥接到远程的 `loglens mcp`（远程主机需把 `loglens` 装入 PATH，`LOGLENS_*` 环境变量在远程设置）：

```json
{
  "mcpServers": {
    "loglens-remote": {
      "command": "ssh",
      "args": ["user@your-server", "loglens", "mcp"]
    }
  }
}
```

**方式 3 · 局域网直连（不建隧道，从零六步）**：同一局域网内服务器直连，按顺序照抄。`192.168.1.50` 与 `loglens-lan` 是示例，按实际替换。

1. **日志主机准备二进制**（在编译机执行）：

```bash
cd loglens && CGO_ENABLED=0 go build ./cmd/loglens
scp loglens user@192.168.1.50:~/
ssh user@192.168.1.50 'sudo mv ~/loglens /usr/local/bin/'
```

2. **生成强 token、前台起 serve**（在日志主机执行；`sudo` 用于读 `/var/log` 下 root 才可读的目录，无此需要可去掉）：

```bash
token=$(openssl rand -hex 32)
echo "copy me: $token"                                    # 抄下来，第 5 步用
# sudo 默认丢弃环境变量(env_reset),须用 `sudo env` 传 LOGLENS_WHITELIST_DIRS;--token 是命令行参数不受影响
sudo env LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs \
  /usr/local/bin/loglens serve --addr 192.168.1.50:8787 --token "$token"
```

3. **日志主机本机自测**（另开一个终端）：

```bash
# 不带 token → 应返回 401
curl -s -o /dev/null -w 'no-token: %{http_code}\n' -X POST http://127.0.0.1:8787/mcp -d '{}'
# 带 token → 不应是 401
curl -s -o /dev/null -w 'with-token: %{http_code}\n' \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -X POST http://127.0.0.1:8787/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

4. **确认内网 IP、放行防火墙**（日志主机）：

```bash
ip addr | grep 'inet '          # 找到内网 IP，如 192.168.1.50
sudo ufw allow 8787/tcp         # Ubuntu；RHEL: sudo firewall-cmd --add-port=8787/tcp
```

5. **本地注册**（开发机；把 IP 与 token 换成实际的）：

```bash
claude mcp add --scope user --transport http loglens-lan \
  http://192.168.1.50:8787/mcp --header "Authorization: Bearer 你的token"
```

或直接写入 `~/.claude.json` 顶层 `mcpServers`：

```json
{
  "mcpServers": {
    "loglens-lan": {
      "type": "http",
      "url": "http://192.168.1.50:8787/mcp",
      "headers": {
        "Authorization": "Bearer 你的token"
      }
    }
  }
}
```

`loglens-lan` 只是注册名，可随意改；同一份配置内唯一即可。

6. **验证并常驻**：

```bash
claude mcp get loglens-lan          # 连接 OK 即通
```

确认后把第 2 步的前台进程换成 systemd 常驻（Ctrl-C 停掉，再建单元）：

```ini
# /etc/systemd/system/loglens.service
[Unit]
Description=LogLens MCP server
After=network.target

[Service]
Environment=LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs
ExecStart=/usr/local/bin/loglens serve --addr 192.168.1.50:8787 --token 你的token
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now loglens
```

**排障速查**：

| 现象 | 原因 |
|---|---|
| curl 返回 401 | token 不符 / 没带 `Authorization` header |
| 连接拒绝 | 端口未监听、`--addr` 绑了别的 IP、防火墙未放行 |
| 日志主机本机能连、别机超时 | 防火墙未放行 / 绑定仍是 `127.0.0.1`（忘改 addr） |

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

### 配置放在哪里

Claude Code 的 MCP 服务器可注册在三个作用域（均适用于本配置）：

| 作用域 | 写入文件 | 生效范围 | 适用场景 |
|---|---|---|---|
| `project` | 项目根目录 `.mcp.json` | 所有打开该项目的人，可随 git 共享 | 团队共享 |
| `user` | `~/.claude.json` 顶层 `mcpServers` | 自己 · 所有项目 | **个人日常使用（推荐）** |
| `local`（默认） | `~/.claude.json` 的 `projects/<路径>` | 自己 · 仅当前目录 | 本仓库内调试 |

- **个人用 → `user`**：一次注册，任何项目下都能让 Claude 用 loglens 查日志，且无 trust / 审批链路。
- **团队共享 → `project`**：写入 `.mcp.json` 并提交 git。注意两点：首次链接会弹审批确认；批准后其中的 `command` 将以你的身份在本机执行，clone 来路不明的仓库时先审查其 `.mcp.json`。
- **VS Code 扩展与 CLI 读同一批文件**，无独立入口；VS Code 自带的 `.vscode/mcp.json`（Copilot 原生格式）不被读取。

等价于上方 JSON 的一条命令（`--scope user`）：

```bash
claude mcp add --scope user loglens -- loglens mcp \
  -e LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs \
  -e LOGLENS_MAX_LINES=2000 \
  -e LOGLENS_MAX_BYTES=10485760 \
  -e LOGLENS_TIMEOUT_MS=15000
```

> **注意**：`command: "loglens"` 要求 `loglens` 已装入 PATH。否则请写绝对路径 `"command": "/path/to/loglens"`，或 `CGO_ENABLED=0 go build ./cmd/loglens && cp loglens ~/.local/bin/`。

## 安全上限速查

所有文件访问都经过白名单校验（含符号链接解析，防路径穿越 / 软链逃逸）；每次查询强制受限于：

- `LOGLENS_WHITELIST_DIRS`：允许访问的日志目录，逗号分隔，**禁止访问白名单以外路径**
- `LOGLENS_MAX_LINES`：单次查询最大返回行数
- `LOGLENS_MAX_BYTES`：单次读取最大字节上限
- `LOGLENS_MAX_FILES`：`list_log_files` 单次最多返回的文件数（默认 5000）
- `LOGLENS_TIMEOUT_MS`：单次查询超时，超时直接终止任务并返回已读取的部分

超过任一上限时不报错，而是返回**部分结果 + `truncated` 标记与原因**（查询响应的 `per_file` 数组会说明各文件扫描到哪、`reason` 区分是行数还是字节预算先触顶，以便选择「缩小窗口 / 调整 limit」）。

## 当前 MCP 工具

- `list_log_files`：列出白名单目录下日志文件，支持 glob、递归、识别 gz 轮转文件；`sort: "time"` 按修改时间降序（最新在前）方便找最新日志
- `get_log_slice`：按【起止时间 + 关键词 + 上下文行数】切片日志（核心，自动合并 `base.N[.gz]` 轮转组）。无时间窗时返回最近行（tail），带关键词则返回最近匹配
- `search_logs`：关键词 / 正则检索日志，支持排除关键词、忽略大小写、跨文件。设 `tail_backwards: true` 可从最新文件末尾反向扫描、直接拿到**最近一次命中**（不猜时间窗，适合「最近一次调用某接口」这类诉求；字节预算限制回扫范围，截断结果用 `per_file` 标明覆盖）

> **响应体积护栏**：`max_line_chars` 截断超长单行（标记 `text_truncated`）、`max_result_chars` 限制聚合返回文本，防止一条 100 KB 的 JSON 行或宽关键词洪水撑爆 MCP 响应；truncated / 0 命中时响应带 `summary` 字段给出换参建议。

规划中的新工具见 [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md)。

## 日志时间戳语义（重要）

- 带时区/`Z` 的时间戳（zap、RFC3339、JSON `@timestamp`）按字面时区解析。
- **不带时区的时间戳按服务器本地时区解析**。因此查询边界请与日志文件本身保持一致：日志是 UTC（`Z` 结尾）就在 `start_time`/`end_time` 用带 `Z` 的 RFC3339；日志是本地时间（如 Java `2026-11-10 12:10:03,123`）就用 `2026-11-10 12:10:03` 这类写法，或直接引用日志里出现的时间串。
- `start_time` 支持 `-2h` / `-30m` / `-1d` 相对值、`now`、裸日期、纯时间（今天）。

## 目录结构

```
loglens/
├── cmd/loglens/main.go       # 入口，子命令：mcp（默认）/ serve / tui / version
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