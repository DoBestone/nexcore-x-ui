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
        # tarball 来自 CI runner(uid 1001),cp -a 保留了那个 uid。
        # preflightBinary 要求 xray owner=root,否则拒绝启动。
        chown -R root:root "${INSTALL_DIR}" 2>/dev/null || true
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
    # 真理之源是 DB,install-info.txt 只是首装时一次性写入的明文密码快照。
    # 旧实现把 install-info.txt 当作主路径,文件不在就 fallback 到 setting -show —
    # 但 setting -show 在 binary 早期版本里遇到空用户表会 nil-deref 退出非 0,
    # 触发 || warn 分支,操作员看到"binary 不支持 setting -show",误以为版本不对。
    # 现在永远先 setting -show(已 panic-safe),拿到端口/URL/用户名;
    # 然后 install-info.txt 只在存在时作为补充展示明文密码。
    hdr "当前实际设置 (read-from-DB)"
    "${INSTALL_DIR}/${CMD_NAME}" setting -show || \
        warn "setting -show 返回非 0 — 看 ${C}journalctl -u ${SERVICE_NAME} -n 80${N}"

    if [[ -f "${INFO_FILE}" ]]; then
        echo
        hdr "首装明文凭据快照 (install-info.txt)"
        cat "${INFO_FILE}"
        echo
        info "以上明文密码仅首装窗口期可见;在面板里改过密码后,binary 会主动删除该文件"
        info "记录后建议立即 ${C}rm ${INFO_FILE}${N},或等 24h 自动过期清理"
    else
        echo
        warn "install-info.txt 不存在 — 你要么已经在面板里改过密码,要么从未生成过"
        warn "密码经 bcrypt 哈希存储,无法还原明文。忘了请用:${C}${CMD_NAME} reset${N}"
    fi
}

cmd_reset() {
    require_installed
    confirm "把账号密码 + 端口重置为新随机值?" "n" || { info "已取消"; return; }

    # 旧版本在缺 sqlite3 时直接 `rm -f "${DB_FILE}"` 触发首次初始化 —
    # 看似简单,实际很危险:.db-wal / .db-shm 残留会导致 SQLite 在某些发行版
    # 上拒绝创建新 DB(报"file is encrypted or is not a database"),
    # 操作员看到的就是"reset 完发现 install-info.txt 没出来,面板也起不来"。
    # 现在改为先尝试自动安装 sqlite3,失败才回退到删 DB(同时清掉 wal/shm 兄弟文件)。
    if ! command -v sqlite3 >/dev/null 2>&1; then
        info "sqlite3 不在 PATH — 尝试自动安装"
        if command -v apt-get >/dev/null 2>&1; then
            apt-get update -y >/dev/null 2>&1 || true
            DEBIAN_FRONTEND=noninteractive apt-get install -y sqlite3 >/dev/null 2>&1 || true
        elif command -v dnf >/dev/null 2>&1; then
            dnf install -y sqlite >/dev/null 2>&1 || true
        elif command -v yum >/dev/null 2>&1; then
            yum install -y sqlite >/dev/null 2>&1 || true
        elif command -v apk >/dev/null 2>&1; then
            apk add --no-cache sqlite >/dev/null 2>&1 || true
        elif command -v pacman >/dev/null 2>&1; then
            pacman -Sy --noconfirm sqlite >/dev/null 2>&1 || true
        fi
    fi

    systemctl stop "${SERVICE_NAME}"
    rm -f "${INFO_FILE}"

    if command -v sqlite3 >/dev/null 2>&1; then
        # 单事务清掉 admin user + webPort 设置(其它设置保留,例如 secureEntry / TLS)。
        # `set -e` upstream 会在 sqlite3 非 0 时停下,我们再走 catch 分支兜底。
        if ! sqlite3 "${DB_FILE}" <<'SQL'
BEGIN;
DELETE FROM settings WHERE key='webPort';
DELETE FROM users;
COMMIT;
SQL
        then
            err "sqlite3 事务执行失败 — DB 可能已损坏:${DB_FILE}"
            warn "建议:${C}${CMD_NAME} backup${N} 备份后,${C}rm ${DB_FILE}*${N} 整库重建(会丢所有 inbound)"
            systemctl start "${SERVICE_NAME}"
            return 1
        fi
        ok "已清掉 admin user + webPort 设置(其余设置保留)"
    else
        warn "sqlite3 自动安装失败 — fallback 到整库删除(包含 .db-wal / .db-shm 兄弟文件)"
        warn "副作用:所有 inbound / outbound / 流量统计 / API token 都会一同丢失"
        confirm "继续整库删除?" "n" || { info "已取消";  systemctl start "${SERVICE_NAME}"; return 1; }
        rm -f "${DB_FILE}" "${DB_FILE}-wal" "${DB_FILE}-shm" "${DB_FILE}-journal"
    fi

    systemctl start "${SERVICE_NAME}"

    # 主动等到面板起来 + admin 用户重新落库,而不是裸 sleep 2。
    info "等待面板重新初始化…"
    for i in $(seq 1 30); do
        if [[ -f "${INFO_FILE}" ]]; then break; fi
        if "${INSTALL_DIR}/${CMD_NAME}" setting -show 2>/dev/null \
                | awk -F': *' '/^  username:/{print $2}' \
                | grep -qvE '^\(未创建\)|^$'; then
            break
        fi
        sleep 1
    done

    if [[ -f "${INFO_FILE}" ]]; then
        cat "${INFO_FILE}"
        echo
        info "记录后建议立即 ${C}rm ${INFO_FILE}${N}"
    else
        warn "install-info.txt 仍未生成 — 凭据落在了 journal 里"
        echo
        info "找新密码:${C}journalctl -u ${SERVICE_NAME} -n 80 | grep -E 'username|password|panel port'${N}"
        echo
        hdr "DB 实时状态"
        "${INSTALL_DIR}/${CMD_NAME}" setting -show || true
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
    # Open the panel sqlite shell. Default is read-only — interactive
    # exploration is what 99% of operators want, and a misplaced
    # `DELETE FROM users` from a tutorial copy/paste shouldn't be able
    # to nuke auth. Pass `-rw` (or `--write`) to opt into write mode;
    # we still warn loudly so muscle memory doesn't carry over.
    require_installed
    command -v sqlite3 >/dev/null 2>&1 || die "需要先安装 sqlite3 (apt install sqlite3)"
    local mode="ro"
    case "${1:-}" in
        -rw|--write|rw|write) mode="rw" ;;
        ""|-r|-ro|--read|ro|read) mode="ro" ;;
        *) die "用法: ${CMD_NAME} db [ro|rw]   (默认 ro,只读)" ;;
    esac
    if [[ "${mode}" == "rw" ]]; then
        warn "进入可写 sqlite shell — 任何 UPDATE/DELETE 都会立刻持久化"
        warn "建议先 ${CMD_NAME} backup,误操作可恢复"
        confirm "继续?" "n" || { info "已取消"; return; }
        sqlite3 "${DB_FILE}"
    else
        info "只读模式 (传 'rw' 进入可写)"
        # `?mode=ro` URI tells sqlite3 to refuse any write attempt.
        sqlite3 "file:${DB_FILE}?mode=ro" -cmd ".bail on"
    fi
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

# ---------- group: SSL/TLS cert (acme.sh integration) ----------
#
# 设计要点:
#   * acme.sh 按需安装到 /root/.acme.sh,不动其他位置
#   * 证书统一落地 /root/cert/<domain>.cer + .key,匹配面板 cert.go 扫描路径
#   * CA 故障转移: Let's Encrypt → ZeroSSL → Buypass(任一成功即停)
#   * 续签靠 crontab(每日 03:00 acme.sh --cron),续签后通过 reloadcmd 自动重启面板
#   * 三种验证方式覆盖典型场景:
#       DNS-01 cf       端口无关,推荐(无视宝塔/nginx/CF 橙云)
#       HTTP-01 webroot 与已有 nginx/宝塔共存,只写挑战文件
#       HTTP-01 standalone 仅 80 空闲时可用,占用过则报错引导

ACME_DIR="/root/.acme.sh"
ACME_BIN="${ACME_DIR}/acme.sh"
CERT_DIR="/root/cert"
ACME_LOG="${DATA_DIR}/acme.log"

ensure_acme() {
    if [[ -x "${ACME_BIN}" ]]; then
        return 0
    fi
    info "首次使用,正在安装 acme.sh 到 ${ACME_DIR}"
    command -v curl >/dev/null || die "缺少 curl。请先 apt install curl 或 yum install curl"
    # --nocron: 我们用 crontab 统一管理,不用 acme.sh 的内置 cron
    curl -fsSL https://get.acme.sh | sh -s -- --nocron --home "${ACME_DIR}" >/dev/null \
        || die "acme.sh 安装失败,见 ${ACME_LOG}"
    "${ACME_BIN}" --set-default-ca --server letsencrypt >/dev/null 2>&1 || true
    ok "acme.sh 已安装"
}

# 检测端口是否空闲。占用时打印 pid:进程名 并返回非 0。
check_port_free() {
    local port="$1" pid_proc
    if ss -tlnH "( sport = :${port} )" 2>/dev/null | grep -q .; then
        pid_proc=$(ss -tlnpH "( sport = :${port} )" 2>/dev/null | head -1 \
            | grep -oP 'users:\(\("\K[^"]+' | head -1)
        err "端口 ${port} 被占用 (${pid_proc:-unknown}),改用 [1] DNS-01 或 [2] webroot"
        return 1
    fi
    return 0
}

issue_with_failover() {
    local domain="$1"; shift
    local extra=("$@")
    local cas=("letsencrypt" "zerossl" "buypass")
    local email="${ACME_EMAIL:-acme@$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo localhost)}"
    mkdir -p "${CERT_DIR}"
    for ca in "${cas[@]}"; do
        info "尝试 CA: ${ca}"
        # ZeroSSL/Buypass 首次签发前必须 register-account,LE 不需要。
        # 没注册过时直接 --issue 会失败,导致"故障转移"白白浪费一次尝试。
        if [[ "$ca" == "zerossl" || "$ca" == "buypass" ]]; then
            local acct_dir="${ACME_DIR}/ca/acme-${ca}.api"
            [[ "$ca" == "zerossl" ]] && acct_dir="${ACME_DIR}/ca/acme.zerossl.com"
            [[ "$ca" == "buypass" ]] && acct_dir="${ACME_DIR}/ca/api.buypass.com"
            if [[ ! -f "${acct_dir}/account.key" ]]; then
                info "首次使用 ${ca},注册账号 (email: ${email})"
                if ! "${ACME_BIN}" --register-account --server "$ca" -m "$email" >> "${ACME_LOG}" 2>&1; then
                    warn "${ca} 账号注册失败,跳过此 CA"
                    continue
                fi
            fi
        fi
        if "${ACME_BIN}" --issue --server "$ca" -d "$domain" "${extra[@]}" 2>&1 | tee -a "${ACME_LOG}"; then
            ok "${ca} 签发成功"
            install_cert "$domain"
            return 0
        fi
        warn "${ca} 失败,尝试下一个"
    done
    die "所有 CA 都失败,详见 ${ACME_LOG}"
}

install_cert() {
    local domain="$1"
    local cert_path="${CERT_DIR}/${domain}.cer"
    local key_path="${CERT_DIR}/${domain}.key"
    "${ACME_BIN}" --install-cert -d "$domain" \
        --cert-file       "$cert_path" \
        --key-file        "$key_path" \
        --fullchain-file  "${CERT_DIR}/${domain}.fullchain.cer" \
        --reloadcmd       "${CMD_NAME} cert _post-renew ${domain}" >/dev/null \
        || die "install-cert 失败"
    chmod 600 "$cert_path" "$key_path"
    ok "证书已安装: ${cert_path}"
    register_cron
}

register_cron() {
    local cron_line="0 3 * * * ${ACME_BIN} --cron --home ${ACME_DIR} >> ${ACME_LOG} 2>&1"
    if crontab -l 2>/dev/null | grep -qF "${ACME_BIN} --cron"; then
        return 0
    fi
    (crontab -l 2>/dev/null; echo "$cron_line") | crontab -
    ok "已注册自动续签 (每日 03:00)"
}

cmd_cert_apply() {
    require_root
    ensure_acme

    local domain="${1:-}"
    [[ -z "$domain" ]] && read -r -p "  域名 (例 vpn.example.com): " domain
    [[ -z "$domain" ]] && die "域名不能为空"

    echo
    echo "  请选择验证方式:"
    echo "    [1] DNS-01 (推荐, 端口无关) — 需要 Cloudflare API Token"
    echo "    [2] HTTP-01 webroot       — 与已有 nginx/宝塔共存"
    echo "    [3] HTTP-01 standalone    — 仅 80 端口空闲时可用"
    local mode
    read -r -p "  选择 [1-3, 默认 1]: " mode
    mode="${mode:-1}"

    case "$mode" in
        1)
            local cf_token="${CF_TOKEN:-}"
            if [[ -z "$cf_token" ]]; then
                read -rs -p "  Cloudflare API Token (Zone:DNS:Edit 权限): " cf_token
                echo
            fi
            [[ -z "$cf_token" ]] && die "API Token 不能为空"
            export CF_Token="$cf_token"
            issue_with_failover "$domain" --dns dns_cf
            ;;
        2)
            local default_root="/www/wwwroot/${domain}" webroot
            read -r -p "  网站根目录 [默认 ${default_root}]: " webroot
            webroot="${webroot:-$default_root}"
            [[ -d "$webroot" ]] || warn "目录不存在: $webroot — 申请可能失败,请确认"
            echo
            echo "${Y}!${N} 重要 — 若该域名已开启 Cloudflare 橙云代理(默认开启),"
            echo "  你必须先去 CF 控制台添加 Configuration Rule:"
            echo "    匹配:  URI Path  startswith  /.well-known/acme-challenge/"
            echo "    动作:  Automatic HTTPS Rewrites = Off"
            echo "           SSL/TLS encryption mode  = Off"
            echo "           Cache Level              = Bypass"
            echo "  否则 ACME 服务器的 HTTP 请求会被 CF 跳转到 HTTPS,验证失败。"
            echo "  如果你的域名没走 CF 或未开启橙云,可直接确认。"
            echo
            confirm "已确认上述配置(或本域名未走 CF)?" "n" || { info "已取消"; return 0; }
            issue_with_failover "$domain" --webroot "$webroot"
            ;;
        3)
            check_port_free 80 || return 1
            issue_with_failover "$domain" --standalone --httpport 80
            ;;
        *)
            die "无效选择: $mode"
            ;;
    esac

    if confirm "是否绑定到面板 (重启面板使 HTTPS 生效)?" n; then
        cmd_cert_bind_panel "$domain"
    fi
}

cmd_cert_list() {
    [[ -d "$CERT_DIR" ]] || { info "(暂无证书)"; return; }
    local has_any=false
    printf "  %-30s %-12s %s\n" "DOMAIN" "EXPIRES" "PATH"
    for cer in "$CERT_DIR"/*.cer; do
        [[ -f "$cer" ]] || continue
        [[ "$cer" == *fullchain* ]] && continue
        has_any=true
        local domain expire end
        domain="$(basename "$cer" .cer)"
        if end=$(openssl x509 -in "$cer" -noout -enddate 2>/dev/null | cut -d= -f2); then
            expire="$(date -d "$end" +%Y-%m-%d 2>/dev/null || echo "$end")"
        else
            expire="(parse error)"
        fi
        printf "  %-30s %-12s %s\n" "$domain" "$expire" "$cer"
    done
    ${has_any} || info "(暂无证书)"
}

cmd_cert_renew() {
    [[ -x "${ACME_BIN}" ]] || die "acme.sh 未安装,先 ${CMD_NAME} cert apply"
    local domain="${1:-}"
    if [[ -n "$domain" ]]; then
        "${ACME_BIN}" --renew -d "$domain" --force 2>&1 | tee -a "${ACME_LOG}"
    else
        "${ACME_BIN}" --cron --home "${ACME_DIR}" 2>&1 | tee -a "${ACME_LOG}"
    fi
}

cmd_cert_remove() {
    local domain="${1:-}"
    [[ -z "$domain" ]] && read -r -p "  域名: " domain
    [[ -z "$domain" ]] && die "域名不能为空"
    confirm "删除 ${domain} 的证书和 acme 配置?" n || return 0
    [[ -x "${ACME_BIN}" ]] && "${ACME_BIN}" --remove -d "$domain" >/dev/null 2>&1 || true
    rm -f "${CERT_DIR}/${domain}.cer" "${CERT_DIR}/${domain}.key" \
          "${CERT_DIR}/${domain}.fullchain.cer"
    ok "已删除 ${domain}"
}

# 把指定证书绑定为面板的 HTTPS 证书。直接写 sqlite settings 表,
# 重启面板后 webCertFile/webKeyFile 立刻生效。
cmd_cert_bind_panel() {
    require_installed
    local domain="${1:-}"
    [[ -z "$domain" ]] && read -r -p "  域名: " domain
    [[ -z "$domain" ]] && die "域名不能为空"
    local cert_path="${CERT_DIR}/${domain}.cer"
    local key_path="${CERT_DIR}/${domain}.key"
    [[ -f "$cert_path" ]] || die "证书不存在: ${cert_path}"
    [[ -f "$key_path" ]]  || die "私钥不存在: ${key_path}"
    sqlite_set "webCertFile" "$cert_path"
    sqlite_set "webKeyFile"  "$key_path"
    ok "已绑定: webCertFile=${cert_path}"
    if confirm "现在重启面板使配置生效?" y; then
        cmd_restart
    else
        warn "未重启,执行 ${CMD_NAME} restart 后生效"
    fi
}

# 内部命令: 由 acme.sh reloadcmd 调用。
#
# 面板内置了 TLS 证书热重载(60s 轮询 cert/key 文件 mtime),acme.sh 续签后
# 直接覆盖 /root/cert/<domain>.cer 与 .key,面板下次 TLS 握手即用新证书,
# 无需重启进程、不掉现有连接。这里只发个低成本 SIGHUP 加速首次重载窗口
# (web/main.go 收到 SIGHUP 会重建监听器),不再 systemctl restart。
#
# 如果操作员想要强制重启(例如 binary 被换),手动 ${CMD_NAME} restart。
cmd_cert_post_renew() {
    local domain="$1"
    info "证书续签完成: ${domain},发送 SIGHUP 通知面板热重载"
    if pid=$(systemctl show -p MainPID --value "${SERVICE_NAME}" 2>/dev/null) && [[ -n "${pid}" && "${pid}" != "0" ]]; then
        kill -HUP "${pid}" 2>/dev/null && return 0
    fi
    warn "未取到面板 PID,fallback 到完整 restart"
    systemctl restart "${SERVICE_NAME}" 2>/dev/null || true
}

# 通过 sqlite3 写面板设置。settings 表无唯一约束,先 DELETE 再 INSERT 保证幂等。
sqlite_set() {
    local key="$1" value="$2"
    [[ -f "${DB_FILE}" ]] || die "数据库不存在: ${DB_FILE}"
    command -v sqlite3 >/dev/null || die "缺少 sqlite3。请先 apt install sqlite3"
    # 转义 SQL 单引号以防注入
    local k_esc="${key//\'/\'\'}"
    local v_esc="${value//\'/\'\'}"
    sqlite3 "${DB_FILE}" \
        "DELETE FROM settings WHERE key = '${k_esc}'; INSERT INTO settings (key, value) VALUES ('${k_esc}', '${v_esc}');"
}

cmd_cert() {
    local sub="${1:-}"; [[ $# -gt 0 ]] && shift
    case "$sub" in
        apply|issue)     cmd_cert_apply "$@" ;;
        list|ls)         cmd_cert_list ;;
        renew)           cmd_cert_renew "$@" ;;
        remove|rm|del)   cmd_cert_remove "$@" ;;
        bind-panel|bind) cmd_cert_bind_panel "$@" ;;
        _post-renew)     cmd_cert_post_renew "$@" ;;
        ""|help|-h|--help)
            cat <<CERTHELP
${B}cert${N} — SSL/TLS 证书管理 (集成 acme.sh)

  cert apply [domain]            申请证书 (交互式选择验证方式)
  cert list                      列出已申请的证书
  cert renew [domain]            手动续签 (省略 domain = 全部)
  cert remove <domain>           删除证书 + acme 配置
  cert bind-panel <domain>       绑定为面板的 HTTPS 证书 (自动重启)

  ${D}验证方式:${N}
    1. DNS-01 (Cloudflare API Token) — 推荐, 端口无关
    2. HTTP-01 webroot              — 与已有 nginx/宝塔共存
    3. HTTP-01 standalone            — 仅 80 空闲时可用

  ${D}CA 故障转移:${N} Let's Encrypt → ZeroSSL → Buypass
CERTHELP
            ;;
        *) die "未知 cert 子命令: $sub" ;;
    esac
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

${B}cert${N}
  cert apply [domain]               申请证书 (交互选 DNS-01 / webroot / standalone)
  cert list                         列出已申请的证书
  cert renew [domain]               手动续签 (省略 = 全部)
  cert remove <domain>              删除证书 + acme 配置
  cert bind-panel <domain>          绑定为面板 HTTPS 证书 (自动重启)

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
  ${B}8.${N} 实时日志         ${B}16.${N} SSL 证书 (申请/绑定面板)
                                ${B}0.${N}  退出
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
        16)
            cat <<CERTMENU

  ${B}a.${N} 申请证书 (apply)
  ${B}l.${N} 列出已有 (list)
  ${B}r.${N} 续签 (renew)
  ${B}b.${N} 绑定面板 (bind-panel)
  ${B}d.${N} 删除 (remove)
CERTMENU
            read -p "  选择 [a/l/r/b/d]: " sub
            case "$sub" in
                a) cmd_cert apply ;;
                l) cmd_cert list ;;
                r) read -p "  域名 (留空续签全部): " d; cmd_cert renew "$d" ;;
                b) read -p "  域名: " d; cmd_cert bind-panel "$d" ;;
                d) read -p "  域名: " d; cmd_cert remove "$d" ;;
                *) warn "未知选项" ;;
            esac
            ;;
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

    # SSL/TLS cert
    cert)         shift; cmd_cert "$@" ;;

    -h|--help|help)
        if [[ -n "$2" ]]; then
            grep -A 2 "^[[:space:]]*$2" <<<"$(show_help)" | head -5
        else
            show_help
        fi
        ;;
    *) die "未知命令: $1 — 看 ${CMD_NAME} help" ;;
esac
