#!/bin/bash
# NexCore x-ui · 管理脚本(systemd-only,无 Docker 路径)
set -eo pipefail

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

GH_OWNER="${GH_OWNER:-DoBestone}"
GH_REPO="${GH_REPO:-nexcore-x-ui}"
DATA_DIR="${DATA_DIR:-/etc/x-ui}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/x-ui}"

LOG()  { echo -e "${green}[x-ui]${plain} $*"; }
WARN() { echo -e "${yellow}[x-ui]${plain} $*"; }
ERR()  { echo -e "${red}[x-ui]${plain} $*" >&2; }

require_root() {
    [[ $EUID -ne 0 ]] && { ERR "必须以 root 运行"; exit 1; }
}

require_installed() {
    if [[ ! -f /etc/systemd/system/x-ui.service ]]; then
        ERR "x-ui 未安装,先执行 \`x-ui install\`"
        exit 2
    fi
}

confirm() {
    local prompt="$1"
    local default="${2:-n}"
    local ans
    read -p "${prompt} [y/n,默认 ${default}]: " ans
    ans="${ans:-${default}}"
    [[ "${ans}" == "y" || "${ans}" == "Y" ]]
}

# ---------- subcommands ----------

cmd_install() {
    LOG "拉取并执行最新 install.sh"
    bash <(curl -fsSL "https://raw.githubusercontent.com/${GH_OWNER}/${GH_REPO}/main/install.sh") "$@"
}

cmd_update() {
    require_installed
    LOG "在线更新到最新版本"
    bash <(curl -fsSL "https://raw.githubusercontent.com/${GH_OWNER}/${GH_REPO}/main/install.sh") "$@"
}

cmd_uninstall() {
    require_installed
    confirm "确定卸载 x-ui? (会同时删除 ${DATA_DIR})" "n" || { LOG "已取消"; return; }
    systemctl stop x-ui || true
    systemctl disable x-ui || true
    rm -f /etc/systemd/system/x-ui.service
    rm -rf "${INSTALL_DIR}" "${DATA_DIR}" /usr/bin/x-ui
    systemctl daemon-reload
    LOG "已卸载"
}

cmd_start()   { require_installed; systemctl start x-ui;   systemctl status x-ui --no-pager; }
cmd_stop()    { require_installed; systemctl stop x-ui;    systemctl status x-ui --no-pager || true; }
cmd_restart() { require_installed; systemctl restart x-ui; systemctl status x-ui --no-pager; }
cmd_status()  { require_installed; systemctl status x-ui --no-pager; }
cmd_log()     { require_installed; journalctl -u x-ui -n 200 --no-pager -e; }
cmd_log_tail(){ require_installed; journalctl -u x-ui -f; }

cmd_enable()  { require_installed; systemctl enable  x-ui && LOG "开机自启已启用"; }
cmd_disable() { require_installed; systemctl disable x-ui && LOG "开机自启已禁用"; }

cmd_setting() {
    require_installed
    "${INSTALL_DIR}/x-ui" setting "$@"
}

cmd_show_info() {
    require_installed
    if [[ -f "${DATA_DIR}/install-info.txt" ]]; then
        cat "${DATA_DIR}/install-info.txt"
    else
        WARN "install-info.txt 不存在(可能你已经手动改过密码或删除了文件)"
        "${INSTALL_DIR}/x-ui" setting -show
    fi
}

cmd_reset_creds() {
    require_installed
    confirm "重置账号密码 + 端口为新随机值?" "n" || { LOG "已取消"; return; }
    systemctl stop x-ui
    rm -f "${DATA_DIR}/install-info.txt"
    # 让 binary 在下次启动时再次走 first-run 路径:
    # 1) 删 webPort 设置 → 触发新随机端口
    # 2) 删用户表 → 触发新随机账号
    sqlite3 "${DATA_DIR}/x-ui.db" "DELETE FROM settings WHERE key='webPort'; DELETE FROM users;" 2>/dev/null || \
        WARN "若没装 sqlite3,请手动 rm ${DATA_DIR}/x-ui.db 后重启"
    systemctl start x-ui
    sleep 2
    cat "${DATA_DIR}/install-info.txt" 2>/dev/null
}

# ---------- 交互菜单 ----------

show_menu() {
    echo
    echo -e "${green}NexCore x-ui · 管理菜单${plain}"
    cat <<MENU
  0. 退出
  1. 安装/更新
  2. 卸载
  3. 启动 / 4. 停止 / 5. 重启 / 6. 状态 / 7. 最近日志 / 8. 实时日志
  9. 开机自启:启用 / 10. 开机自启:禁用
 11. 显示登录信息(install-info.txt)
 12. 重置账号密码 + 端口为新随机值
 13. 命令行设置(转 setting)
MENU
    read -p "选择: " ch
    case "$ch" in
        0) exit 0 ;;
        1) cmd_install ;;
        2) cmd_uninstall ;;
        3) cmd_start ;;
        4) cmd_stop ;;
        5) cmd_restart ;;
        6) cmd_status ;;
        7) cmd_log ;;
        8) cmd_log_tail ;;
        9) cmd_enable ;;
       10) cmd_disable ;;
       11) cmd_show_info ;;
       12) cmd_reset_creds ;;
       13) shift; cmd_setting "$@" ;;
        *) WARN "未知选项";;
    esac
}

# ---------- 入口 ----------

require_root

if [[ $# -eq 0 ]]; then
    show_menu
    exit 0
fi

case "$1" in
    install)   shift; cmd_install   "$@" ;;
    update)    shift; cmd_update    "$@" ;;
    uninstall)        cmd_uninstall ;;
    start)            cmd_start ;;
    stop)             cmd_stop ;;
    restart)          cmd_restart ;;
    status)           cmd_status ;;
    log)              cmd_log ;;
    log-tail|tail)    cmd_log_tail ;;
    enable)           cmd_enable ;;
    disable)          cmd_disable ;;
    info|show)        cmd_show_info ;;
    reset)            cmd_reset_creds ;;
    setting)   shift; cmd_setting   "$@" ;;
    -h|--help|help)
        cat <<'USAGE'
x-ui [command]

  install [version]    安装(可选指定版本号 tag)
  update  [version]    在线升级到最新或指定版本
  uninstall            卸载(连数据一起删)
  start | stop | restart | status
  log                  最近 200 行日志
  log-tail             实时跟踪日志
  enable | disable     开机自启
  info                 显示 install-info.txt(端口/账号/密码)
  reset                把账号密码 + 端口重置为新随机值
  setting [-flags]     等价于 /usr/local/x-ui/x-ui setting

无参数运行进入交互菜单。
USAGE
        ;;
    *) ERR "未知命令: $1 (查看 \`x-ui help\`)"; exit 2 ;;
esac
