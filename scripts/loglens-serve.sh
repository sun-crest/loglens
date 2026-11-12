#!/usr/bin/env bash
set -euo pipefail

# loglens-serve.sh — 把 LogLens 的 serve 部署成 systemd 常驻服务（在【日志所在主机】上运行）。
#
#   安装(需 sudo):生成/复用 token → 写入 /etc/systemd/system/loglens.service → 启动并开机自启 → 打印访问 token
#   token 保存在 /etc/loglens/token(0600),本地 Claude Code 配置时用 `sudo loglens-serve.sh token` 查看
#
# 用法:
#   sudo LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs \
#       ./loglens-serve.sh install [--addr 0.0.0.0:8787] [--token abc...]
#   sudo ./loglens-serve.sh uninstall
#   sudo ./loglens-serve.sh status          # 服务状态与最近日志(排障)
#   sudo ./loglens-serve.sh token           # 打印访问 token

BIN="$(command -v loglens || true)"
SERVICE_NAME=loglens
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
TOKEN_FILE=/etc/loglens/token

ADDR="${LOGLENS_ADDR:-0.0.0.0:8787}"
TOKEN=""

usage() {
    cat >&2 <<'EOF'
用法:
    sudo LOGLENS_WHITELIST_DIRS=/dir1,/dir2 \
      ./loglens-serve.sh install [--addr 0.0.0.0:8787] [--token abc...]
    sudo ./loglens-serve.sh uninstall
    sudo ./loglens-serve.sh status
    sudo ./loglens-serve.sh token

子命令:
    install    生成/复用 token → 写 systemd 单元 → enable --now 启动 → 打印 token
    uninstall  停止并移除服务(token 文件保留,想彻底清除: rm /etc/loglens/token)
    status     查看服务状态与最近日志(排障)
    token      打印保存的访问 token(本地 Claude Code 的 Authorization 用)

参数:
    --addr    监听地址,默认 0.0.0.0:8787(局域网直连)。只本机用改 127.0.0.1:8787。
    --token   指定固定 token;缺省时复用 /etc/loglens/token,否则自动生成并保存。

环境变量:
    LOGLENS_WHITELIST_DIRS   允许访问的日志目录,逗号分隔(必填)
    LOGLENS_ADDR             等价于 --addr(可省)
EOF
}

need_root() {
    [ "$(id -u)" -eq 0 ] || { echo "错误:此命令需要 root(用 sudo)" >&2; exit 1; }
}

install() {
    need_root
    [ "$BIN" ] || { echo "错误:loglens 不在 PATH;先把二进制拷到日志主机并放入 PATH(如 /usr/local/bin/loglens)" >&2; exit 1; }
    [ -n "${LOGLENS_WHITELIST_DIRS:-}" ] || { echo "错误:需要 LOGLENS_WHITELIST_DIRS=/dir1,/dir2" >&2; exit 1; }

    # token:优先 --token,其次已存文件,最后自动生成。
    if [ -z "$TOKEN" ] && [ -s "$TOKEN_FILE" ]; then
        TOKEN="$(cat "$TOKEN_FILE")"
    elif [ -z "$TOKEN" ]; then
        command -v openssl >/dev/null || { echo "错误:需要 openssl 来生成 token" >&2; exit 1; }
        install -d -m 700 /etc/loglens
        TOKEN="$(openssl rand -hex 32)"
        umask 077
        echo "$TOKEN" > "$TOKEN_FILE"
    fi

    cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=LogLens MCP server
After=network.target

[Service]
Environment=LOGLENS_WHITELIST_DIRS=${LOGLENS_WHITELIST_DIRS}
ExecStart=${BIN} serve --addr ${ADDR} --token ${TOKEN}
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable --now "$SERVICE_NAME" >/dev/null

    echo "✓ LogLens serve 已装成常驻服务"
    echo "  服务:   $SERVICE_NAME"
    echo "  地址:   http://$ADDR/mcp"
    echo "  白名单: $LOGLENS_WHITELIST_DIRS"
    echo "  token:  $TOKEN   (本地配置用;已存 $TOKEN_FILE,可随时 'sudo loglens-serve.sh token' 查看)"
    echo "  排障:   sudo loglens-serve.sh status"
}

uninstall() {
    need_root
    systemctl disable --now "$SERVICE_NAME" 2>/dev/null || true
    rm -f "$SERVICE_FILE"
    systemctl daemon-reload
    echo "✓ 已停止并移除服务 $SERVICE_NAME(token 文件保留:$TOKEN_FILE)"
}

status() {
    need_root
    systemctl --no-pager --lines=20 status "$SERVICE_NAME"
}

show_token() {
    need_root
    cat "$TOKEN_FILE" 2>/dev/null || echo "(尚未安装或未生成 token)"
}

[ "$#" -ge 1 ] || { usage; exit 1; }
CMD="$1"; shift

while [ "$#" -gt 0 ]; do
    case "$1" in
        --addr)  ADDR="${2:?--addr 需要一个值}"; shift 2 ;;
        --token) TOKEN="${2:?--token 需要一个值}"; shift 2 ;;
        *) echo "未知参数: $1" >&2; exit 1 ;;
    esac
done

case "$CMD" in
    install)   install ;;
    uninstall) uninstall ;;
    status)    status ;;
    token)     show_token ;;
    *) usage; exit 1 ;;
esac