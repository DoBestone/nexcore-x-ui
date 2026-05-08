#!/bin/bash
# NexCore x-ui · install (fresh install only — for upgrades use update.sh
# or `nexcore-x-ui update`).
#
# Usage:
#   bash <(curl -Ls https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh)
#   bash <(curl -Ls https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh) v1.2.3
#   bash <(curl -Ls https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh) --force
#
# Variables that override the defaults:
#   GH_OWNER  GH_REPO  REPO_BRANCH  INSTALL_DIR  DATA_DIR  CMD_NAME
#
# Coexistence with original `x-ui` (vaxilu/x-ui or 3x-ui) is by design:
# we install to /usr/local/nexcore-x-ui, store data in /etc/nexcore-x-ui,
# and register a separate systemd unit `nexcore-x-ui.service`. None of
# these collide with /usr/local/x-ui, /etc/x-ui or x-ui.service.
#
# This script does NOT do upgrades. If a prior install is detected we exit
# with a clear pointer to update.sh — silently re-running install.sh on a
# live deployment had been the cause of "首装却走升级分支" reports because
# DATA_DIR was preserved (good!) but show_credentials then couldn't tell
# whether the binary was a first-run or a noop-restart.

set -eo pipefail

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
blue='\033[0;34m'
cyan='\033[0;36m'
plain='\033[0m'

CMD_NAME="${CMD_NAME:-nexcore-x-ui}"
GH_OWNER="${GH_OWNER:-DoBestone}"
GH_REPO="${GH_REPO:-nexcore-x-ui}"
REPO_BRANCH="${REPO_BRANCH:-main}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/${CMD_NAME}}"
DATA_DIR="${DATA_DIR:-/etc/${CMD_NAME}}"
SERVICE_FILE="/etc/systemd/system/${CMD_NAME}.service"

FORCE=false
TARGET_VERSION=""
for arg in "$@"; do
    case "$arg" in
        --force|-f)  FORCE=true ;;
        v*|V*)       TARGET_VERSION="$arg" ;;
        *)           TARGET_VERSION="$arg" ;;
    esac
done

# ---------- output helpers ----------

step() { echo -e "${blue}▸${plain} $*"; }
ok()   { echo -e "${green}✓${plain} $*"; }
warn() { echo -e "${yellow}!${plain} $*" >&2; }
err()  { echo -e "${red}✗${plain} $*" >&2; }
die()  { err "$*"; exit 1; }

# ---------- preflight: blocking checks ----------

[[ $EUID -ne 0 ]] && die "必须以 root 身份运行此脚本"

if ! command -v systemctl >/dev/null 2>&1; then
    die "本机没有 systemd,无法安装。仅支持 Debian/Ubuntu/CentOS 等带 systemd 的发行版"
fi

case $(uname -m) in
    x86_64|x64|amd64) ARCH="amd64" ;;
    aarch64|arm64)    ARCH="arm64" ;;
    armv7l|armv7)     ARCH="armv7" ;;
    s390x)            ARCH="s390x" ;;
    *) die "未支持的 CPU 架构:$(uname -m)" ;;
esac

# Detect existing install. install.sh is fresh-install-only; if the operator
# wants to update they should use update.sh / `nexcore-x-ui update` which
# leaves DATA_DIR (DB + creds + cert paths) untouched. If they really want
# to clobber the install dir, --force makes that explicit.
existing_install=false
if [[ -f "${SERVICE_FILE}" ]] || [[ -x "${INSTALL_DIR}/${CMD_NAME}" ]]; then
    existing_install=true
fi
if ${existing_install} && ! ${FORCE}; then
    err "检测到 ${CMD_NAME} 已安装(存在 ${SERVICE_FILE} 或 ${INSTALL_DIR}/${CMD_NAME})。"
    echo
    echo -e "  ${cyan}日常升级请用:${plain}"
    echo -e "    bash <(curl -Ls https://raw.githubusercontent.com/${GH_OWNER}/${GH_REPO}/main/update.sh)"
    echo -e "    或:  ${CMD_NAME} update"
    echo
    echo -e "  ${cyan}强制重装(会覆盖 ${INSTALL_DIR},但保留 ${DATA_DIR} 内的 DB):${plain}"
    echo -e "    bash <(curl -Ls https://raw.githubusercontent.com/${GH_OWNER}/${GH_REPO}/main/install.sh) --force"
    echo
    echo -e "  ${cyan}彻底卸载再装(会清掉 DB / 凭据 / 自定义设置):${plain}"
    echo -e "    ${CMD_NAME} uninstall  # 然后重跑 install.sh"
    exit 1
fi

# ---------- preflight: warnings (not blocking) ----------

# /tmp 空间检查 — 30MB tarball + 30MB 解压 ≈ 100MB 安全余量
free_tmp_kb=$(df -k /tmp 2>/dev/null | awk 'NR==2{print $4}')
if [[ -n "${free_tmp_kb}" && "${free_tmp_kb}" -lt 102400 ]]; then
    warn "/tmp 可用空间 < 100MB($((free_tmp_kb / 1024))MB),下载/解压可能失败"
fi

# DATA_DIR 父目录可写
data_parent="$(dirname "${DATA_DIR}")"
if [[ ! -w "${data_parent}" ]]; then
    die "无法写入 ${data_parent} — 检查 root 权限或自定义 DATA_DIR=/可写路径 重试"
fi

# 内存检查(警告)— 1H1G 是底线;<512M 跑 xray 容易 OOM
mem_kb=$(awk '/MemTotal/{print $2}' /proc/meminfo 2>/dev/null || echo 0)
if [[ "${mem_kb}" -gt 0 && "${mem_kb}" -lt 524288 ]]; then
    warn "系统内存 < 512MB($((mem_kb / 1024))MB),xray + 面板 + sqlite 同时运行可能 OOM"
fi

# 与 vaxilu/x-ui / 3x-ui 共存提醒 — 我们的端口/路径都不冲突,但用户体验上常困惑
if [[ -f /etc/systemd/system/x-ui.service ]] || [[ -x /usr/local/x-ui/x-ui ]]; then
    warn "检测到 x-ui (vaxilu / 3x-ui) 也在本机。两个面板路径独立,但端口请避免冲突(默认都是 54321)"
fi

echo -e "${green}架构:${plain} ${ARCH}"

# ---------- deps: auto-install missing tools ----------

ensure_pkg() {
    # Try to install a list of packages. Idempotent: already-installed pkgs are
    # quietly skipped. We keep this robust against the three big package managers
    # because the original script silently fell back to "skip" for non-Ubuntu
    # distros, leaving users with a half-broken install when a tool was missing.
    local pkgs="$*"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -y >/dev/null 2>&1 || true
        DEBIAN_FRONTEND=noninteractive apt-get install -y ${pkgs} >/dev/null 2>&1 || return 1
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y ${pkgs} >/dev/null 2>&1 || return 1
    elif command -v yum >/dev/null 2>&1; then
        yum install -y ${pkgs} >/dev/null 2>&1 || return 1
    elif command -v apk >/dev/null 2>&1; then
        apk add --no-cache ${pkgs} >/dev/null 2>&1 || return 1
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm ${pkgs} >/dev/null 2>&1 || return 1
    else
        return 1
    fi
}

# Verify every required tool is on PATH; install any that's missing. We do
# this one-by-one (rather than a single bulk call) so the error message
# pinpoints the exact tool that fell through.
required_tools=(curl tar awk grep sed)
optional_tools=(sha256sum)  # busybox lacks sha256sum on some images
missing=()
for t in "${required_tools[@]}"; do
    command -v "$t" >/dev/null 2>&1 || missing+=("$t")
done
if ! command -v sha256sum >/dev/null 2>&1; then
    # Try installing coreutils (apt) / coreutils (dnf/yum) — the package name
    # is the same on all major distros for this tool.
    missing+=("coreutils")
fi
if [[ ${#missing[@]} -gt 0 ]]; then
    step "安装缺失依赖: ${missing[*]}"
    if ! ensure_pkg "${missing[@]}"; then
        warn "自动安装失败 — 请手动 \`apt install ${missing[*]}\` 或等价命令后重试"
        # don't die; some images lack a recognized package manager but
        # already have the tools (e.g. installed by ops), so let the
        # individual command checks below decide
    fi
fi
# Last-mile sanity: bail loudly if we still can't run the core flow
for t in curl tar awk grep sed sha256sum; do
    command -v "$t" >/dev/null 2>&1 || die "依赖 '$t' 仍然不可用,无法继续。请手动安装后重试"
done

# ---------- download + verify ----------

resolve_version() {
    if [[ -n "$1" ]]; then
        echo "$1"
        return
    fi
    local v
    v=$(curl -fsSL "https://api.github.com/repos/${GH_OWNER}/${GH_REPO}/releases/latest" \
        | grep -E '"tag_name":' \
        | sed -E 's/.*"([^"]+)".*/\1/' || true)
    if [[ -z "$v" ]]; then
        die "无法获取最新版本号(可能 GitHub API 限流或仓库尚无 release)"
    fi
    echo "$v"
}

download_release() {
    # Caller captures the tarball path via $(...) — every status line MUST
    # go to stderr or it pollutes the captured variable. Same pattern the
    # original script had; preserved verbatim.
    local version="$1"
    local pkg="nexcore-x-ui-linux-${ARCH}.tar.gz"
    local url="https://github.com/${GH_OWNER}/${GH_REPO}/releases/download/${version}/${pkg}"
    local sum_url="https://github.com/${GH_OWNER}/${GH_REPO}/releases/download/${version}/checksums.txt"
    local dest="/tmp/nexcore-x-ui-${version}-${ARCH}.tar.gz"
    local sum_dest="/tmp/nexcore-x-ui-${version}-checksums.txt"
    step "下载: ${url}" >&2
    if ! curl -fSL --connect-timeout 10 -o "${dest}" "${url}" >&2; then
        rm -f "${dest}"
        die "下载失败 — release ${version} 可能不存在,或 github 网络不通"
    fi
    # SHA256 verification against checksums.txt.
    step "校验 SHA256…" >&2
    if ! curl -fSL --connect-timeout 10 -o "${sum_dest}" "${sum_url}" >&2; then
        rm -f "${dest}"
        die "下载 checksums.txt 失败 — release ${version} 缺少校验文件,拒绝继续"
    fi
    local expected actual
    expected=$(awk -v want="${pkg}" '$2 == want || $2 == "*"want {print $1; exit}' "${sum_dest}")
    if [[ -z "${expected}" ]]; then
        rm -f "${dest}" "${sum_dest}"
        die "checksums.txt 中找不到 ${pkg} 的条目"
    fi
    actual=$(sha256sum "${dest}" | awk '{print $1}')
    if [[ "${expected}" != "${actual}" ]]; then
        rm -f "${dest}" "${sum_dest}"
        die "SHA256 不匹配!  expected=${expected}  actual=${actual}"
    fi
    rm -f "${sum_dest}"
    ok "  SHA256 OK: ${actual}" >&2
    echo "${dest}"
}

# ---------- install ----------

install_panel() {
    local version="$1"
    local archive="$2"

    if ${existing_install}; then
        step "停止旧服务"
        systemctl stop "${CMD_NAME}" 2>/dev/null || true
    fi

    rm -rf "${INSTALL_DIR}"
    mkdir -p "${INSTALL_DIR}" "${DATA_DIR}"
    chmod 700 "${DATA_DIR}"

    rm -rf "/tmp/${CMD_NAME}-extract"
    mkdir -p "/tmp/${CMD_NAME}-extract"
    tar -xzf "${archive}" -C "/tmp/${CMD_NAME}-extract/"
    if [[ ! -d "/tmp/${CMD_NAME}-extract/${CMD_NAME}" ]]; then
        die "压缩包内容不符合预期(缺少 ${CMD_NAME}/)"
    fi
    cp -a "/tmp/${CMD_NAME}-extract/${CMD_NAME}/." "${INSTALL_DIR}/"
    rm -rf "/tmp/${CMD_NAME}-extract" "${archive}"

    # Force ownership to root:root.
    # Release tarballs are built on GitHub Actions runners (uid 1001), and
    # `cp -a` / `tar -xzf` preserve that uid. xray.preflightBinary then
    # refuses to launch a non-root-owned binary, looping with
    # "owned by uid 1001". This was v1.0.5; the three upgrade paths
    # (update.sh / nexcore-x-ui.sh::cmd_update / service/update.go::ApplyLatest)
    # all chown — install.sh missed it, so a fresh install reproduced
    # the same failure. Keep this line in lockstep with the other three.
    chown -R root:root "${INSTALL_DIR}"

    chmod +x "${INSTALL_DIR}/${CMD_NAME}"
    [[ -d "${INSTALL_DIR}/bin" ]] && chmod +x "${INSTALL_DIR}/bin/"* 2>/dev/null || true
    [[ -f "${INSTALL_DIR}/${CMD_NAME}.sh" ]] && chmod +x "${INSTALL_DIR}/${CMD_NAME}.sh"

    install -m 0755 "${INSTALL_DIR}/${CMD_NAME}.sh" "/usr/bin/${CMD_NAME}"
    install -m 0644 "${INSTALL_DIR}/${CMD_NAME}.service" "${SERVICE_FILE}"

    # Pin DB path via a systemd drop-in. Editing the unit file in place
    # with `sed -i ... \n[Service]\nEnvironment=...` is fragile: any
    # special character in DATA_DIR (|, /, &) breaks the s-command, and
    # operators that customize the unit on next upgrade lose their edits.
    # Drop-ins compose cleanly with future unit-file changes and survive
    # `install-mode 0644` rewrites of the parent unit.
    local override_dir="/etc/systemd/system/${CMD_NAME}.service.d"
    mkdir -p "${override_dir}"
    chmod 755 "${override_dir}"
    cat > "${override_dir}/10-data-dir.conf" <<EOF
# Managed by install.sh — pins NEXCORE_DB_PATH so install-info.txt lands in
# the DATA_DIR install.sh chose. Hand-edit at your own risk.
[Service]
Environment=NEXCORE_DB_PATH=${DATA_DIR}/${CMD_NAME}.db
EOF
    chmod 644 "${override_dir}/10-data-dir.conf"

    systemctl daemon-reload
    systemctl enable "${CMD_NAME}" >/dev/null 2>&1
    # `restart` rather than `start` because a half-stopped service from a
    # previous run could otherwise wedge the unit into a dependency loop.
    systemctl reset-failed "${CMD_NAME}" 2>/dev/null || true
    systemctl restart "${CMD_NAME}"
}

# ---------- post-install verification ----------

# 等服务真起来,而不是盲目 sleep 2。systemd is-active 在 Type=simple 下
# 几乎瞬时就 true,但 binary 内部的 InitDB + RunFirstRunSetup 还需要 1-3s
# 才完成第一行 fmt.Println。给 30s 上限以容忍冷盘 SQLite 创建。
wait_for_active() {
    local max="${1:-30}"
    for i in $(seq 1 "${max}"); do
        if systemctl is-active --quiet "${CMD_NAME}"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# 等到面板端口在监听 / install-info.txt 出现 / setting -show 能读到 admin user
# 为止,任意一个达成就算"准备好了"。最长 30s。
wait_for_ready() {
    local info_file="${DATA_DIR}/install-info.txt"
    for i in $(seq 1 30); do
        if [[ -f "${info_file}" ]]; then
            return 0
        fi
        # `setting -show` 返回退出码 0 即视为面板侧已经就绪,即便 install-info.txt
        # 因为某种原因(磁盘满/sandbox)没写出来 — DB 是真理之源,文件是补充。
        if "${INSTALL_DIR}/${CMD_NAME}" setting -show >/dev/null 2>&1; then
            # ensure user row really exists; setting -show prints even on empty DB
            local username
            username=$("${INSTALL_DIR}/${CMD_NAME}" setting -show 2>/dev/null \
                | awk -F': *' '/^  username:/{print $2; exit}')
            if [[ -n "${username}" && "${username}" != "(未创建)" ]]; then
                return 0
            fi
        fi
        sleep 1
    done
    return 1
}

show_credentials() {
    local info_file="${DATA_DIR}/install-info.txt"
    echo
    echo -e "${green}═════════════════════════════════════════════${plain}"
    echo -e "${green}  NexCore x-ui 已部署${plain}"
    echo -e "${green}═════════════════════════════════════════════${plain}"

    # Source-of-truth-first: DB-live readback. install-info.txt is shown as
    # a supplemental snapshot when present (it has the plaintext password,
    # which DB-live cannot recover post-bcrypt).
    if [[ -f "${info_file}" ]]; then
        cat "${info_file}"
        echo
        echo -e "  ${yellow}重要:${plain} 记录后请删除该文件 — \`rm ${info_file}\`"
        echo -e "  (24h 后面板会自动清理它)"
    else
        warn "install-info.txt 未生成 — 走 DB 实时读"
        echo
        # setting -show 实时读 DB,显示 username + 端口 + 完整 URL
        "${INSTALL_DIR}/${CMD_NAME}" setting -show || \
            warn "setting -show 失败 — 试试: ${cyan}journalctl -u ${CMD_NAME} -n 80${plain}"
        echo
        echo -e "  ${yellow}注:${plain} 密码 bcrypt 不可逆,如已忘记请用 \`${CMD_NAME} reset\` 重置"
    fi

    echo
    echo -e "${green}管理命令:${plain}"
    echo "  ${CMD_NAME}                    交互菜单"
    echo "  ${CMD_NAME} doctor             健康检查(强烈建议立即跑一次)"
    echo "  systemctl status ${CMD_NAME}   服务状态"
    echo "  journalctl -u ${CMD_NAME} -f   实时日志"
    echo
}

# 装完跑一次 doctor 主动暴露问题(端口未监听 / health 不通 / 用户表空 等)。
# doctor 会自己打印每一项,我们只关心退出码:非 0 就给操作员一条恢复指令。
run_doctor() {
    if "${INSTALL_DIR}/${CMD_NAME}.sh" doctor >/dev/null 2>&1; then
        ok "doctor: 全部检查通过"
        return 0
    fi
    warn "doctor 报告了问题 — 完整输出:"
    "${INSTALL_DIR}/${CMD_NAME}.sh" doctor || true
    echo
    echo -e "${cyan}常见恢复方案:${plain}"
    echo -e "  - 服务未起来:  ${cyan}journalctl -u ${CMD_NAME} -n 100${plain} 看启动错误"
    echo -e "  - 端口被占用:  ${cyan}${CMD_NAME} port <new-port>${plain} 改端口后再 ${cyan}${CMD_NAME} restart${plain}"
    echo -e "  - 想从头来:    ${cyan}${CMD_NAME} reset${plain}(重置账号/端口)或 ${cyan}${CMD_NAME} uninstall${plain}"
    return 1
}

# ---------- main ----------

echo -e "${green}NexCore x-ui · install${plain}"
VERSION="$(resolve_version "${TARGET_VERSION}")"
echo -e "${green}版本:${plain} ${VERSION}"

ARCHIVE="$(download_release "${VERSION}")"
install_panel "${VERSION}" "${ARCHIVE}"

step "等待 systemd 服务激活(最长 30s)…"
if ! wait_for_active 30; then
    err "服务未在 30s 内进入 active 状态。dump 最近日志:"
    journalctl -u "${CMD_NAME}" -n 60 --no-pager || true
    die "首次启动失败 — 修复后再 \`systemctl restart ${CMD_NAME}\` 或重跑本脚本"
fi
ok "服务已激活"

step "等待面板首次初始化完成(InitDB + 创建 admin)…"
if wait_for_ready; then
    ok "面板就绪"
else
    warn "30s 内没看到 admin 用户落库 — 凭据可能在 journal 而不是 install-info.txt 里"
    echo -e "  最近日志:"
    journalctl -u "${CMD_NAME}" -n 30 --no-pager | grep -E 'NexCore|first-run|panel port|username|password' || \
        journalctl -u "${CMD_NAME}" -n 30 --no-pager
fi

show_credentials
run_doctor || true
