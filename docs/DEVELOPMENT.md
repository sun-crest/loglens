# LogLens 开发与测试

面向在本仓库源码上开发的维护者 / 贡献者 / CI。要把 LogLens 部署给用户用，见 README「远程部署」；版本管理与发版见 `docs/RELEASING.md`。

## 构建

```bash
CGO_ENABLED=0 go build ./cmd/loglens
```

- **静态编译**：产物不依赖系统 glibc，可拷到任意 Linux（含旧版发行版）。
- 产物落在仓库根 `loglens`，`loglens version` 可查版本身份。
- 发行打包用的同一命令加上 `-trimpath -ldflags "-s -w"`（见 `scripts/package.sh`）。

## 测试

```bash
go test ./...       # 单元 + 集成测试
go test -race ./... # 竞态检测
```

通过官方 SDK 客户端走**完整 MCP 握手**——stdio 与 HTTP 两套传输都有集成覆盖（`internal/mcp/*_test.go`），被测的是真实 wire 协议而非内部函数；测试策略详见 `docs/DESIGN.md`。**新增工具 / 查询路径必须补测试并保证 race 全绿。**

## serve 快速冒烟

起一个需要 token 的本地 MCP HTTP 端点并验证握手：

```bash
LOGLENS_WHITELIST_DIRS=/tmp/logs loglens serve --addr 127.0.0.1:8787 --token dev
```

```bash
curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer dev' \
  -H 'Content-Type: application/json' \
  -X POST http://127.0.0.1:8787/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

不带 token 应得 401，带 token 应得 200。生产形态的部署 / 连通冒烟见 README「远程部署」。

## 关联文档

- 架构与测试策略：`docs/DESIGN.md`
- 需求与路线图：`docs/REQUIREMENTS.md`
- 版本管理与发版：`docs/RELEASING.md`