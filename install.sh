#!/bin/bash
# NexCore x-ui · install / upgrade
#
# Usage:
#   bash <(curl -Ls https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh)
#   bash <(curl -Ls https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh) v1.2.3
#
# Variables that override the defaults:
#   GH_OWNER  GH_REPO  REPO_BRANCH  INSTALL_DIR  DATA_DIR  CMD_NAME
#
# Coexistence with original `x-ui` (vaxilu/x-ui or 3x-ui) is by design:
# we install to /usr/local/nexcore-x-ui, store data in /etc/nexcore-x-ui,
# and register a separate systemd unit `nexcore-x-ui.service`. None of
# these collide with /usr/local/x-ui, /etc/x-ui or x-ui.service.
#
# What this script does NOT do (deliberately):
#   - It will NOT prompt you for a username/password/port. The binary picks
#     random values on first start; we display them at the end.
#   - It will NOT use --no-check-certificate or other TLS shortcuts.
#   - It will NOT bundle a Docker workflow. systemd only.

set -eo pipefail

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

CMD_NAME="${CMD_NAME:-nexcore-x-ui}"
GH_OWNER="${GH_OWNER:-DoBestone}"
GH_REPO="${GH_REPO:-nexcore-x-ui}"
REPO_BRANCH="${REPO_BRANCH:-main}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/${CMD_NAME}}"
DATA_DIR="${DATA_DIR:-/etc/${CMD_NAME}}"
SERVICE_FILE="/etc/systemd/system/${CMD_NAME}.service"

# ---------- preflight ----------

[[ $EUID -ne 0 ]] && {
    echo -e "${red}必须以 root 身份运行此脚本${plain}" >&2
    exit 1
}

if ! command -v systemctl >/dev/null 2>&1; then
    echo -e "${red}本机没有 systemd,无法安装。仅支持 Debian/Ubuntu/CentOS 等带 systemd 的发行版${plain}" >&2
    exit 1
fi

case $(uname -m) in
    x86_64|x64|amd64) ARCH="amd64" ;;
    aarch64|arm64)    ARCH="arm64" ;;
    armv7l|armv7)     ARCH="armv7" ;;
    s390x)            ARCH="s390x" ;;
    *) echo -e "${red}未支持的 CPU 架构:$(uname -m)${plain}"; exit 1 ;;
esac
echo -e "${green}架构:${plain} ${ARCH}"

# ---------- deps ----------

install_deps() {
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -y >/dev/null
        apt-get install -y wget curl tar ca-certificates >/dev/null
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y wget curl tar ca-certificates >/dev/null
    elif command -v yum >/dev/null 2>&1; then
        yum install -y wget curl tar ca-certificates >/dev/null
    else
        echo -e "${yellow}未识别包管理器,跳过依赖安装(需自行确保 wget/curl/tar 可用)${plain}"
    fi
}

# ---------- download ----------

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
        echo -e "${red}无法获取最新版本号(可能 GitHub API 限流或仓库尚无 release)${plain}" >&2
        exit 1
    fi
    echo "$v"
}

download_release() {
    # NOTE: this function returns the downloaded path on stdout via the final
    # `echo "${dest}"` so the caller can capture it with $(...). All progress
    # / log output therefore MUST go to stderr (>&2), or the caller's variable
    # will end up containing the log string concatenated with the path.
    local version="$1"
    local pkg="nexcore-x-ui-linux-${ARCH}.tar.gz"
    local url="https://github.com/${GH_OWNER}/${GH_REPO}/releases/download/${version}/${pkg}"
    local sum_url="https://github.com/${GH_OWNER}/${GH_REPO}/releases/download/${version}/checksums.txt"
    local dest="/tmp/nexcore-x-ui-${version}-${ARCH}.tar.gz"
    local sum_dest="/tmp/nexcore-x-ui-${version}-checksums.txt"
    echo -e "${green}下载:${plain} ${url}" >&2
    if ! curl -fSL --connect-timeout 10 -o "${dest}" "${url}" >&2; then
        echo -e "${red}下载失败,请检查 release 是否存在${plain}" >&2
        exit 1
    fi
    # Verify SHA256 against checksums.txt published in the same release.
    # An attacker who can swap a release asset gets RCE on every node that
    # runs install.sh — checksums.txt closes that window. Failure is fatal.
    echo -e "${green}校验 SHA256…${plain}" >&2
    if ! curl -fSL --connect-timeout 10 -o "${sum_dest}" "${sum_url}" >&2; then
        echo -e "${red}下载 checksums.txt 失败 — release ${version} 缺少校验文件,拒绝继续${plain}" >&2
        rm -f "${dest}"
        exit 1
    fi
    if ! command -v sha256sum >/dev/null 2>&1; then
        echo -e "${red}本机缺少 sha256sum,无法校验,拒绝继续${plain}" >&2
        rm -f "${dest}" "${sum_dest}"
        exit 1
    fi
    local expected actual
    expected=$(awk -v want="${pkg}" '$2 == want || $2 == "*"want {print $1; exit}' "${sum_dest}")
    if [[ -z "${expected}" ]]; then
        echo -e "${red}checksums.txt 中找不到 ${pkg} 的条目${plain}" >&2
        rm -f "${dest}" "${sum_dest}"
        exit 1
    fi
    actual=$(sha256sum "${dest}" | awk '{print $1}')
    if [[ "${expected}" != "${actual}" ]]; then
        echo -e "${red}SHA256 不匹配!  expected=${expected}  actual=${actual}${plain}" >&2
        rm -f "${dest}" "${sum_dest}"
        exit 1
    fi
    rm -f "${sum_dest}"
    echo -e "${green}  SHA256 OK: ${actual}${plain}" >&2
    echo "${dest}"
}

# ---------- install ----------

install_panel() {
    local version="$1"
    local archive="$2"

    systemctl stop "${CMD_NAME}" 2>/dev/null || true

    rm -rf "${INSTALL_DIR}"
    mkdir -p "${INSTALL_DIR}" "${DATA_DIR}"
    chmod 700 "${DATA_DIR}"

    rm -rf "/tmp/${CMD_NAME}-extract"
    mkdir -p "/tmp/${CMD_NAME}-extract"
    tar -xzf "${archive}" -C "/tmp/${CMD_NAME}-extract/"
    if [[ ! -d "/tmp/${CMD_NAME}-extract/${CMD_NAME}" ]]; then
        echo -e "${red}压缩包内容不符合预期(缺少 ${CMD_NAME}/)${plain}" >&2
        exit 1
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
    systemctl enable "${CMD_NAME}"
    systemctl restart "${CMD_NAME}"
}

# ---------- post-install banner ----------

show_credentials() {
    local info_file="${DATA_DIR}/install-info.txt"
    for _ in 1 2 3 4 5; do
        [[ -f "${info_file}" ]] && break
        sleep 1
    done
    echo
    echo -e "${green}═════════════════════════════════════════════${plain}"
    echo -e "${green}  NexCore x-ui 已部署,登录信息如下${plain}"
    echo -e "${green}═════════════════════════════════════════════${plain}"
    if [[ -f "${info_file}" ]]; then
        cat "${info_file}"
    else
        echo -e "${yellow}install-info.txt 未生成(也许是升级而非首次安装)${plain}"
        echo "  - 查看现状: ${CMD_NAME} setting -show"
        echo "  - 重置账号: ${CMD_NAME} setting -username <X> -password <Y>"
    fi
    echo
    echo -e "${green}管理命令:${plain}"
    echo "  ${CMD_NAME}                   交互菜单"
    echo "  systemctl status ${CMD_NAME}  系统服务状态"
    echo "  journalctl -u ${CMD_NAME} -f  实时日志"
    echo
    local local_ip
    local_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    [[ -z "${local_ip}" ]] && local_ip="<server-ip>"
    local port
    port=$(grep -oE 'panel port: [0-9]+' "${info_file}" 2>/dev/null | awk '{print $3}' | head -1)
    [[ -n "${port}" ]] && echo -e "${green}打开:${plain} http://${local_ip}:${port}"
    echo
}

# ---------- main ----------

echo -e "${green}NexCore x-ui · install / upgrade${plain}"
install_deps
VERSION="$(resolve_version "${1:-}")"
echo -e "${green}版本:${plain} ${VERSION}"
ARCHIVE="$(download_release "${VERSION}")"
install_panel "${VERSION}" "${ARCHIVE}"
show_credentials
