#!/bin/bash
# NexCore x-ui · CLI management tool
#
# 设计原则:
#   1. 命令按用途分组(service / install / creds / data / panel / advanced)
#   2. 大动作必须 confirm,且接受 -y / --yes 跳过
#   3. 默认输出可读;脚本调用走 --quiet / --json 时只输出关键值
#   4. 与 vaxilu/x-ui 完全独立 — 安装路径、systemd unit、命令名都不冲突
#
# 用法:
#   nexcore-x-ui              交互菜单
#   nexcore-x-ui <command>    直接执行
#   nexcore-x-ui help         所有命令
#   nexcore-x-ui help <cmd>   单条命令的详情
# `set -u` (nounset) catches accidental rm -rf "${UNSET_VAR}" / similar
# expansions that could nuke unrelated paths. pipefail makes piped failures
# visible. Together they're strict but match the safety the CLI demands —
# the panel runs as root.
set -euo pipefail

# ---------- defaults (env-overridable) ----------

CMD_NAME="${CMD_NAME:-nexcore-x-ui}"
GH_OWNER="${GH_OWNER:-DoBestone}"
GH_REPO="${GH_REPO:-nexcore-x-ui}"
DATA_DIR="${DATA_DIR:-/etc/${CMD_NAME}}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/${CMD_NAME}}"
SERVICE_NAME="${CMD_NAME}"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
DB_FILE="${DATA_DIR}/${CMD_NAME}.db"
INFO_FILE="${DATA_DIR}/install-info.txt"

ASSUME_YES=false
QUIET=false

# ---------- output helpers ----------

if [[ -t 1 ]]; then
    R=$'\033[0;31m'; G=$'\033[0;32m'; Y=$'\033[0;33m'; B=$'\033[0;34m'; C=$'\033[0;36m'; D=$'\033[2m'; N=$'\033[0m'
else
    R=''; G=''; Y=''; B=''; C=''; D=''; N=''
fi
hdr()  { ${QUIET} || echo "${B}── $* ──${N}"; }
ok()   { ${QUIET} || echo "${G}✓${N} $*"; }
info() { ${QUIET} || echo "${C}·${N} $*"; }
warn() { ${QUIET} || echo "${Y}!${N} $*" >&2; }
err()  { echo "${R}✗${N} $*" >&2; }
die()  { err "$*"; exit 1; }

# ---------- preflight ----------

require_root()      { [[ $EUID -eq 0 ]] || die "必须以 root 运行"; }
require_installed() { [[ -f "${SERVICE_FILE}" ]] || die "${CMD_NAME} 未安装。运行:${CMD_NAME} install"; }

confirm() {
    local msg="$1" default="${2:-n}"
    ${ASSUME_YES} && return 0
    local ans
    read -p "${Y}?${N} ${msg} [y/n,默认 ${default}]: " ans
    ans="${ans:-${default}}"
    [[ "${ans}" =~ ^[yY] ]]
}

# ---------- group: service control ----------

cmd_start()    { require_installed; systemctl start   "${SERVICE_NAME}"; ok "started";  cmd_status; }
cmd_stop()     { require_installed; systemctl stop    "${SERVICE_NAME}"; ok "stopped"; }
cmd_restart()  { require_installed; systemctl restart "${SERVICE_NAME}"; ok "restarted"; cmd_status; }
cmd_enable()   { require_installed; systemctl enable  "${SERVICE_NAME}" >/dev/null && ok "开机自启已启用"; }
cmd_disable()  { require_installed; systemctl disable "${SERVICE_NAME}" >/dev/null && ok "开机自启已禁用"; }
cmd_status() {
    require_installed
    systemctl status "${SERVICE_NAME}" --no-pager --lines=0 | head -8 || true
}

cmd_log() {
    require_installed
    local n="${1:-200}"
    journalctl -u "${SERVICE_NAME}" -n "${n}" --no-pager
}

cmd_log_tail() {
    require_installed
    journalctl -u "${SERVICE_NAME}" -f
}

# ---------- group: install lifecycle ----------

cmd_install() {
    info "首次安装 — 拉取并执行 install.sh"
    bash <(curl -fsSL "https://raw.githubusercontent.com/${GH_OWNER}/${GH_REPO}/main/install.sh") "$@"
}

# update 是日常升级路径,刻意不走 install.sh:
#   - 不动 systemd unit(保留你 Environment= 等自定义)
#   - 不重装系统依赖(apt-get 慢且无意义)
#   - 不动 ${DATA_DIR}/(数据库 + install-info.txt 完整保留)
#   - 只:下载 tarball → stop → 替换二进制+脚本+xray binary → start
# 想做完整重装请改用 \`${CMD_NAME} install\`。
cmd_update() {
    require_installed
    local target="${1:-}"

    # 1. resolve version
    if [[ -z "${target}" ]]; then
        info "查询最新版本"
        target=$(curl -fsSL "https://api.github.com/repos/${GH_OWNER}/${GH_REPO}/releases/latest" \
            | grep -E '"tag_name":' \
            | sed -E 's/.*"([^"]+)".*/\1/' || true)
        [[ -z "${target}" ]] && die "无法获取最新版本(GitHub API 限流?)"
    fi

    # 2. detect arch
    local arch
    case $(uname -m) in
        x86_64|x64|amd64) arch=amd64 ;;
        aarch64|arm64)    arch=arm64 ;;
        armv7l|armv7)     arch=armv7 ;;
        s390x)            arch=s390x ;;
        *) die "未支持的架构 $(uname -m)" ;;
    esac

    local current
    current=$("${INSTALL_DIR}/${CMD_NAME}" -v 2>/dev/null || echo unknown)
    info "当前 ${current} → 目标 ${target} (${arch})"
    if [[ "${target}" == "${current}" || "${target}" == "v${current}" ]]; then
        confirm "已经是 ${current},仍要重装?" "n" || { info "已取消"; return; }
    fi

    # 3. download
    local url="https://github.com/${GH_OWNER}/${GH_REPO}/releases/download/${target}/nexcore-x-ui-linux-${arch}.tar.gz"
    local tmp; tmp=$(mktemp -d -t nexcore-update.XXXXXX)
    trap "rm -rf '${tmp}'" RETURN
    info "下载 ${url}"
    if ! curl -fSL --connect-timeout 10 -o "${tmp}/pkg.tar.gz" "${url}"; then
        die "下载失败"
    fi

    # 4. extract + sanity check
    tar -xzf "${tmp}/pkg.tar.gz" -C "${tmp}/"
    [[ -d "${tmp}/${CMD_NAME}" ]] || die "压缩包结构异常,缺少 ${CMD_NAME}/ 目录"
    [[ -f "${tmp}/${CMD_NAME}/${CMD_NAME}" ]] || die "压缩包缺少二进制 ${CMD_NAME}"

    # 5. stop service, swap files, start
    info "停止服务"
    systemctl stop "${SERVICE_NAME}"

    info "替换二进制 + 脚本"
    install -m 0755 "${tmp}/${CMD_NAME}/${CMD_NAME}"      "${INSTALL_DIR}/${CMD_NAME}"
    install -m 0755 "${tmp}/${CMD_NAME}/${CMD_NAME}.sh"   "${INSTALL_DIR}/${CMD_NAME}.sh"
    install -m 0755 "${INSTALL_DIR}/${CMD_NAME}.sh"       "/usr/bin/${CMD_NAME}"
    if [[ -d "${tmp}/${CMD_NAME}/bin" ]]; then
        # 保留旧 config.json — 业务运行时数据,不属于发行包内容
        local keep=""
        [[ -f "${INSTALL_DIR}/bin/config.json" ]] && keep="${INSTALL_DIR}/bin/config.json"
        rm -rf "${INSTALL_DIR}/bin"
        cp -a "${tmp}/${CMD_NAME}/bin" "${INSTALL_DIR}/bin"
        chmod +x "${INSTALL_DIR}/bin/"* 2>/dev/null || true
        [[ -n "${keep}" ]] && cp "${keep}.bak" "${INSTALL_DIR}/bin/config.json" 2>/dev/null || true
    fi

    # systemd unit:仅当 release 中的 .service 文件 与 当前 已不同时才覆盖,
    # 并且备份旧的(保留任何 Environment= 等手动调整)。日常 update 不动它。
    info "启动服务"
    systemctl start "${SERVICE_NAME}"
    sleep 1

    local new
    new=$("${INSTALL_DIR}/${CMD_NAME}" -v 2>/dev/null || echo unknown)
    ok "升级完成: ${current} → ${new}"
    cmd_status
}

cmd_uninstall() {
    require_installed
    confirm "卸载 ${CMD_NAME} 并删除 ${DATA_DIR}?" "n" || { info "已取消"; return; }
    systemctl stop    "${SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
    rm -f  "${SERVICE_FILE}" "/usr/bin/${CMD_NAME}"
    # Guard the rm -rf behind explicit non-empty paths so an unset env
    # never expands to `rm -rf /`. require_installed already gates this,
    # but defense-in-depth is cheap.
    [[ -n "${INSTALL_DIR}" && "${INSTALL_DIR}" != "/" ]] && rm -rf "${INSTALL_DIR}"
    [[ -n "${DATA_DIR}"    && "${DATA_DIR}"    != "/" ]] && rm -rf "${DATA_DIR}"
    rm -rf "/etc/systemd/system/${CMD_NAME}.service.d"
    systemctl daemon-reload
    ok "已卸载"
}

# ---------- group: credentials ----------

cmd_creds() {
    require_installed
    # install-info.txt 是首次启动时写入的明文凭据快照。一旦在面板内改过密码/端口,
    # binary 会主动删除它(密码 bcrypt 单向无法回写,留着会误导)。
    if [[ -f "${INFO_FILE}" ]]; then
        cat "${INFO_FILE}"
        echo
        info "以上为首次安装快照;面板内改过密码后,该文件会被自动删除"
    else
        warn "install-info.txt 已不存在 — 你已经在面板里改过密码 / 端口"
        warn "密码经 bcrypt 哈希存储,无法还原明文。忘了请用:${CMD_NAME} reset"
        echo
        hdr "当前实际设置"
        "${INSTALL_DIR}/${CMD_NAME}" setting -show 2>/dev/null || \
            warn "binary 不支持 setting -show,看面板设置页"
    fi
}

cmd_reset() {
    require_installed
    confirm "把账号密码 + 端口重置为新随机值?" "n" || { info "已取消"; return; }
    systemctl stop "${SERVICE_NAME}"
    rm -f "${INFO_FILE}"
    if command -v sqlite3 >/dev/null 2>&1; then
        # Wrap in a single transaction so we never leave the DB in a
        # half-cleared state (e.g. users table empty but webPort still
        # set) which would prevent first-run setup from regenerating
        # both. `set -e` upstream catches the sqlite3 non-zero exit.
        if ! sqlite3 "${DB_FILE}" <<'SQL'
BEGIN;
DELETE FROM settings WHERE key='webPort';
DELETE FROM users;
COMMIT;
SQL
        then
            err "数据库重置失败 — 检查 ${DB_FILE} 是否完整"
            systemctl start "${SERVICE_NAME}"
            return 1
        fi
    else
        warn "sqlite3 不存在,直接删除整库以触发首次初始化"
        rm -f "${DB_FILE}"
    fi
    systemctl start "${SERVICE_NAME}"
    sleep 2
    if [[ -f "${INFO_FILE}" ]]; then
        cat "${INFO_FILE}"
    else
        warn "未生成 install-info.txt — 用 'journalctl -u ${SERVICE_NAME} -n 50' 查看启动日志"
    fi
}

cmd_passwd() {
    # Usage: nexcore-x-ui passwd [<username>] [<password>]
    # 任一为空时由 binary 自己拒绝。两者都为空时拒绝(避免误清账号)。
    require_installed
    local user="${1:-}" pass="${2:-}"
    [[ -z "${user}" && -z "${pass}" ]] && die "用法:${CMD_NAME} passwd <username> <password>"
    NEXCORE_USERNAME="${user}" NEXCORE_PASSWORD="${pass}" \
        "${INSTALL_DIR}/${CMD_NAME}" setting -from-env
    ok "credentials updated;将立即生效"
}

cmd_port() {
    require_installed
    local p="${1:-}"
    if [[ -z "${p}" ]]; then
        "${INSTALL_DIR}/${CMD_NAME}" setting -show | grep -E '^port:' | awk '{print $2}'
        return
    fi
    [[ "${p}" =~ ^[0-9]+$ ]] || die "端口必须是数字"
    (( p >= 1 && p <= 65535 )) || die "端口超出范围 1-65535"
    (( p < 1024 )) && warn "端口 ${p} < 1024,需要 root + CAP_NET_BIND_SERVICE 才能绑定"
    confirm "把面板端口改成 ${p} 并重启?" "y" || { info "已取消"; return; }
    "${INSTALL_DIR}/${CMD_NAME}" setting -port "${p}"
    systemctl restart "${SERVICE_NAME}"
    ok "端口 → ${p}"
}

# ---------- group: panel access ----------

panel_port() {
    if [[ -f "${INFO_FILE}" ]]; then
        grep -oE 'panel port: [0-9]+' "${INFO_FILE}" | awk '{print $3}' | head -1
    else
        "${INSTALL_DIR}/${CMD_NAME}" setting -show 2>/dev/null | awk -F': ' '/^port:/{print $2}'
    fi
}

local_ip() {
    hostname -I 2>/dev/null | awk '{print $1}' | head -1
}

cmd_url() {
    require_installed
    local p ip
    p="$(panel_port)"
    ip="$(local_ip)"
    [[ -z "${ip}" ]] && ip="<server-ip>"
    [[ -z "${p}"  ]] && p="<port>"
    echo "http://${ip}:${p}"
}

cmd_open() {
    local u; u="$(cmd_url)"
    info "${u}"
    if   command -v xdg-open >/dev/null 2>&1; then xdg-open "${u}"
    elif command -v open      >/dev/null 2>&1; then open      "${u}"
    else warn "未找到 xdg-open / open;请手动复制 URL"
    fi
}

cmd_magic() {
    require_installed
    local ttl="${1:-600}" note="${2:-cli}"
    local u
    u=$("${INSTALL_DIR}/${CMD_NAME}" magic -ttl "${ttl}" -note "${note}")
    # binary 已经返回完整 URL,直接打印
    if ${QUIET}; then echo "${u}"; else
        ok "magic link (${ttl}s):"
        echo "${u}"
    fi
}

# ---------- group: data ----------

cmd_backup() {
    require_installed
    local out="${1:-./${CMD_NAME}-backup-$(date +%Y%m%d-%H%M%S).tar.gz}"
    warn "备份会包含 install-info.txt(若存在,含明文初始密码)与 sqlite 数据库"
    warn "请妥善保管 ${out},不要上传到公开存储"
    tar -czf "${out}" -C "$(dirname "${DATA_DIR}")" "$(basename "${DATA_DIR}")"
    chmod 600 "${out}"
    ok "备份 → ${out} ($(du -h "${out}" | awk '{print $1}')) [chmod 600]"
}

cmd_restore() {
    require_installed
    local in="$1"
    [[ -f "${in}" ]] || die "找不到备份文件:${in}"

    # Inspect the tarball before extraction. We reject:
    #   - absolute paths (would write to /etc/, /usr/, etc. directly)
    #   - any entry containing ".." (zip-slip, escapes the data dir)
    #   - symlinks / hardlinks (could redirect a later write outside)
    # A malicious backup that survives this filter is restricted to
    # writing files whose names start with the data dir basename.
    local listing
    listing=$(tar -tzf "${in}" 2>/dev/null) || die "tarball 无法读取"
    while IFS= read -r entry; do
        case "${entry}" in
            /*|*../*|*/..|..|"") die "拒绝恢复:tarball 含可疑路径 '${entry}'" ;;
        esac
    done <<<"${listing}"
    if tar -tvzf "${in}" 2>/dev/null | grep -qE '^(l|h)'; then
        die "拒绝恢复:tarball 含符号链接 / 硬链接"
    fi

    confirm "覆盖现有 ${DATA_DIR}?" "n" || { info "已取消"; return; }
    systemctl stop "${SERVICE_NAME}"
    # Extract into a staging dir first so a partial / malicious tarball
    # never half-overwrites the live data dir.
    local stage
    stage=$(mktemp -d -t "${CMD_NAME}-restore.XXXXXX") || die "mktemp 失败"
    trap 'rm -rf "${stage}"' RETURN
    if ! tar -xzf "${in}" -C "${stage}"; then
        systemctl start "${SERVICE_NAME}" 2>/dev/null || true
        die "解压失败"
    fi
    local restored="${stage}/$(basename "${DATA_DIR}")"
    [[ -d "${restored}" ]] || die "tarball 缺少 $(basename "${DATA_DIR}")/ 顶层目录"
    rm -rf "${DATA_DIR}"
    mv "${restored}" "${DATA_DIR}"
    chmod 700 "${DATA_DIR}"
    systemctl start "${SERVICE_NAME}"
    ok "已恢复"
}

cmd_db() {
    require_installed
    command -v sqlite3 >/dev/null 2>&1 || die "需要先安装 sqlite3 (apt install sqlite3)"
    sqlite3 "${DB_FILE}"
}

# ---------- group: doctor ----------

cmd_doctor() {
    hdr "doctor — ${CMD_NAME} 健康检查"
    local fail=0
    _check() {
        local label="$1" ok_msg="$2" fail_msg="$3"
        if eval "$4"; then ok "${label}: ${ok_msg}"
        else err "${label}: ${fail_msg}"; fail=$((fail+1))
        fi
    }
    _check "binary"        "${INSTALL_DIR}/${CMD_NAME}"       "缺失" "[[ -x ${INSTALL_DIR}/${CMD_NAME} ]]"
    _check "service file"  "${SERVICE_FILE}"                  "缺失" "[[ -f ${SERVICE_FILE} ]]"
    _check "data dir"      "${DATA_DIR}"                      "缺失" "[[ -d ${DATA_DIR} ]]"
    _check "database"      "${DB_FILE}"                       "缺失" "[[ -f ${DB_FILE} ]]"
    _check "active"        "${SERVICE_NAME} 正在运行"          "未运行" "systemctl is-active --quiet ${SERVICE_NAME}"
    _check "enabled"       "开机自启"                          "未启用" "systemctl is-enabled --quiet ${SERVICE_NAME}"

    local p; p="$(panel_port)"
    if [[ -n "${p}" ]] && (ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null) | grep -q ":${p} "; then
        ok "panel port ${p} 在监听"
    else
        err "panel port ${p:-(unknown)} 不在监听"; fail=$((fail+1))
    fi

    if curl -fsS --max-time 3 "http://127.0.0.1:${p}/api/v1/health" >/dev/null; then
        ok "/api/v1/health 200"
    else
        err "/api/v1/health 不通"; fail=$((fail+1))
    fi

    echo
    if [[ ${fail} -eq 0 ]]; then ok "all good"; return 0
    else err "${fail} 项问题;${C}journalctl -u ${SERVICE_NAME} -n 200${N} 查看日志"; return 1
    fi
}

# ---------- group: advanced ----------

cmd_setting() {
    require_installed
    "${INSTALL_DIR}/${CMD_NAME}" setting "$@"
}

cmd_exec() {
    require_installed
    "${INSTALL_DIR}/${CMD_NAME}" "$@"
}

cmd_version() {
    if [[ -x "${INSTALL_DIR}/${CMD_NAME}" ]]; then
        "${INSTALL_DIR}/${CMD_NAME}" -v
    else
        echo "(未安装 binary)"
    fi
}

# ---------- help ----------

show_help() {
    cat <<HELP
${C}${CMD_NAME}${N} — NexCore x-ui CLI

${B}service${N}
  start | stop | restart            启停 systemd 服务
  status                            状态摘要
  enable | disable                  开机自启
  log [N]                           最近 N(默认 200)行 journal 日志
  log-tail                          实时跟踪日志

${B}install${N}
  install [tag]                     安装 / 升级到指定 tag(默认 latest)
  update [tag]                      同 install,语义更清晰
  uninstall                         卸载并清理 ${DATA_DIR}

${B}credentials${N}
  creds | info                      显示 install-info.txt
  reset                             重置账号密码 + 端口为新随机值
  passwd <user> <pass>              改账号密码
  port [N]                          显示或设置面板端口

${B}panel access${N}
  url                               打印面板访问 URL
  open                              在本机浏览器打开
  magic [ttl=600] [note=cli]        生成一次性 magic-link 登录 URL

${B}data${N}
  backup [out.tar.gz]               把 ${DATA_DIR} 打包(默认放当前目录)
  restore <in.tar.gz>               从 tarball 恢复
  db                                打开 sqlite shell

${B}health${N}
  doctor                            健康检查(binary/service/port/HTTP)
  version                           运行时版本

${B}advanced${N}
  setting -<flag>                   binary 内置 setting 子命令
  exec <args...>                    binary 直通

${D}全局选项: -y/--yes 跳过 confirm · -q/--quiet 简化输出${N}
${D}env: GH_OWNER GH_REPO INSTALL_DIR DATA_DIR — 覆盖默认路径${N}
HELP
}

# ---------- argument parsing ----------

# extract -y/--yes / -q/--quiet anywhere on the command line
ARGS=()
for a in "$@"; do
    case "$a" in
        -y|--yes)   ASSUME_YES=true ;;
        -q|--quiet) QUIET=true ;;
        *) ARGS+=("$a") ;;
    esac
done
set -- "${ARGS[@]}"

# ---------- 交互菜单(无参数时) ----------

show_menu() {
    echo
    echo "${C}NexCore x-ui · 管理菜单${N}"
    cat <<MENU

  ${B}1.${N} 安装/更新       ${B}9.${N}  开机自启 / 禁用
  ${B}2.${N} 卸载             ${B}10.${N} 显示登录信息
  ${B}3.${N} 启动             ${B}11.${N} 重置账号密码+端口
  ${B}4.${N} 停止             ${B}12.${N} 生成 magic 登录链接
  ${B}5.${N} 重启             ${B}13.${N} 健康检查 (doctor)
  ${B}6.${N} 状态             ${B}14.${N} 备份 / 恢复
  ${B}7.${N} 最近日志         ${B}15.${N} setting 直通
  ${B}8.${N} 实时日志         ${B}0.${N}  退出
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
        9)
            read -p "  enable / disable? " sub
            [[ "$sub" == "enable"  ]] && cmd_enable
            [[ "$sub" == "disable" ]] && cmd_disable
            ;;
        10) cmd_creds ;;
        11) cmd_reset ;;
        12)
            read -p "  ttl 秒数 [600]: " ttl
            cmd_magic "${ttl:-600}" "interactive"
            ;;
        13) cmd_doctor ;;
        14)
            read -p "  backup / restore? " sub
            if [[ "$sub" == "backup" ]]; then cmd_backup
            else read -p "  备份文件路径: " bp; cmd_restore "$bp"
            fi
            ;;
        15) read -p "  setting flags: " flags; cmd_setting $flags ;;
        *) warn "未知选项";;
    esac
}

# ---------- entry ----------

require_root

if [[ ${#ARGS[@]} -eq 0 ]]; then
    show_menu
    exit 0
fi

case "$1" in
    # service
    start)        cmd_start ;;
    stop)         cmd_stop ;;
    restart)      cmd_restart ;;
    status)       cmd_status ;;
    enable)       cmd_enable ;;
    disable)      cmd_disable ;;
    log)          shift; cmd_log "$@" ;;
    log-tail|tail) cmd_log_tail ;;

    # install
    install)      shift; cmd_install   "$@" ;;
    update)       shift; cmd_update    "$@" ;;
    uninstall)    cmd_uninstall ;;

    # credentials
    creds|info|show) cmd_creds ;;
    reset)        cmd_reset ;;
    passwd|password) shift; cmd_passwd "$@" ;;
    port)         shift; cmd_port "$@" ;;

    # panel access
    url)          cmd_url ;;
    open)         cmd_open ;;
    magic)        shift; cmd_magic "$@" ;;

    # data
    backup)       shift; cmd_backup "$@" ;;
    restore)      shift; cmd_restore "$@" ;;
    db)           cmd_db ;;

    # health
    doctor)       cmd_doctor ;;
    version|-v|--version) cmd_version ;;

    # advanced
    setting)      shift; cmd_setting "$@" ;;
    exec)         shift; cmd_exec "$@" ;;

    -h|--help|help)
        if [[ -n "$2" ]]; then
            grep -A 2 "^[[:space:]]*$2" <<<"$(show_help)" | head -5
        else
            show_help
        fi
        ;;
    *) die "未知命令: $1 — 看 ${CMD_NAME} help" ;;
esac
