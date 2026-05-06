#!/bin/bash
# NexCore x-ui · install / upgrade
#
# Usage:
#   bash <(curl -Ls https://raw.githubusercontent.com/<OWNER>/<REPO>/main/install.sh)
#   bash <(curl -Ls https://raw.githubusercontent.com/<OWNER>/<REPO>/main/install.sh) v1.2.3
#
# Variables that override the defaults:
#   GH_OWNER  GH_REPO  REPO_BRANCH  INSTALL_DIR  DATA_DIR
#
# What this script does NOT do anymore (deliberately):
#   - It will NOT prompt you for a username/password/port. The binary picks
#     random values on first start; we display them at the end.
#   - It will NOT use --no-check-certificate or other TLS shortcuts.
#   - It will NOT bundle a Docker workflow. This panel installs as a systemd
#     service only.

set -eo pipefail

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

GH_OWNER="${GH_OWNER:-DoBestone}"
GH_REPO="${GH_REPO:-nexcore-x-ui}"
REPO_BRANCH="${REPO_BRANCH:-main}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/x-ui}"
DATA_DIR="${DATA_DIR:-/etc/x-ui}"
SERVICE_FILE="/etc/systemd/system/x-ui.service"

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
    local version="$1"
    local url="https://github.com/${GH_OWNER}/${GH_REPO}/releases/download/${version}/x-ui-linux-${ARCH}.tar.gz"
    local dest="/tmp/x-ui-${version}-${ARCH}.tar.gz"
    echo -e "${green}下载:${plain} ${url}"
    if ! curl -fSL --connect-timeout 10 -o "${dest}" "${url}"; then
        echo -e "${red}下载失败,请检查 release 是否存在${plain}" >&2
        exit 1
    fi
    echo "${dest}"
}

# ---------- install ----------

install_x_ui() {
    local version="$1"
    local archive="$2"

    systemctl stop x-ui 2>/dev/null || true

    rm -rf "${INSTALL_DIR}"
    mkdir -p "${INSTALL_DIR}" "${DATA_DIR}"
    chmod 700 "${DATA_DIR}"

    tar -xzf "${archive}" -C /tmp/
    if [[ ! -d /tmp/x-ui ]]; then
        echo -e "${red}压缩包内容不符合预期(缺少 x-ui/)${plain}" >&2
        exit 1
    fi
    cp -a /tmp/x-ui/. "${INSTALL_DIR}/"
    rm -rf /tmp/x-ui "${archive}"

    chmod +x "${INSTALL_DIR}/x-ui"
    [[ -d "${INSTALL_DIR}/bin" ]] && chmod +x "${INSTALL_DIR}/bin/"* 2>/dev/null || true
    [[ -f "${INSTALL_DIR}/x-ui.sh" ]] && chmod +x "${INSTALL_DIR}/x-ui.sh"

    install -m 0755 "${INSTALL_DIR}/x-ui.sh" /usr/bin/x-ui
    install -m 0644 "${INSTALL_DIR}/x-ui.service" "${SERVICE_FILE}"

    # Ensure the binary knows where to keep its DB before first start so
    # install-info.txt is created in DATA_DIR for us to grep.
    sed -i.bak '/^Environment=XUI_DB_PATH=/d' "${SERVICE_FILE}"
    sed -i 's|^\[Service\]|[Service]\nEnvironment=XUI_DB_PATH='"${DATA_DIR}"'/x-ui.db|' "${SERVICE_FILE}"
    rm -f "${SERVICE_FILE}.bak"

    systemctl daemon-reload
    systemctl enable x-ui
    systemctl restart x-ui
}

# ---------- post-install banner ----------

show_credentials() {
    local info_file="${DATA_DIR}/install-info.txt"
    # The binary writes install-info.txt on first start; give it a moment.
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
        echo "  - 查看现状: x-ui setting -show"
        echo "  - 重置账号: x-ui setting -username <X> -password <Y>"
    fi
    echo
    echo -e "${green}管理命令:${plain}"
    echo "  x-ui              进入交互菜单(start / stop / log / setting / update / uninstall)"
    echo "  systemctl status x-ui      系统服务状态"
    echo "  journalctl -u x-ui -f      实时日志"
    echo
    local local_ip
    local_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    [[ -z "${local_ip}" ]] && local_ip="<server-ip>"
    local port
    port=$(awk -F'panel port: ' 'NF>1{print $2}' "${info_file}" 2>/dev/null | head -1)
    [[ -z "${port}" ]] && port="$(grep -oE 'panel port: [0-9]+' "${info_file}" 2>/dev/null | awk '{print $3}')"
    [[ -n "${port}" ]] && echo -e "${green}打开:${plain} http://${local_ip}:${port}"
    echo
}

# ---------- main ----------

echo -e "${green}NexCore x-ui · install / upgrade${plain}"
install_deps
VERSION="$(resolve_version "${1:-}")"
echo -e "${green}版本:${plain} ${VERSION}"
ARCHIVE="$(download_release "${VERSION}")"
install_x_ui "${VERSION}" "${ARCHIVE}"
show_credentials
