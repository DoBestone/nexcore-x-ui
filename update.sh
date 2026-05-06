#!/bin/bash
# NexCore x-ui · update
#
# 用法:
#   bash <(curl -Ls https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/update.sh)
#   bash <(curl -Ls https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/update.sh) v1.2.3
#
# 与 install.sh 的区别:
#   - 不动 systemd unit(保留你 Environment= 等自定义)
#   - 不重装系统依赖
#   - 不动 ${DATA_DIR}/(数据库 + install-info.txt 完整保留)
#   - 只:下载 tarball → stop → 替换二进制+脚本+xray binary → start
#
# 想做完整重装请改用 install.sh。
#
# 可用环境变量覆盖默认值:
#   GH_OWNER  GH_REPO  INSTALL_DIR  DATA_DIR  CMD_NAME

set -eo pipefail

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

CMD_NAME="${CMD_NAME:-nexcore-x-ui}"
GH_OWNER="${GH_OWNER:-DoBestone}"
GH_REPO="${GH_REPO:-nexcore-x-ui}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/${CMD_NAME}}"
DATA_DIR="${DATA_DIR:-/etc/${CMD_NAME}}"
SERVICE_NAME="${CMD_NAME}"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

# ---------- preflight ----------

[[ $EUID -ne 0 ]] && {
    echo -e "${red}必须以 root 身份运行此脚本${plain}" >&2
    exit 1
}

if [[ ! -f "${SERVICE_FILE}" ]] || [[ ! -x "${INSTALL_DIR}/${CMD_NAME}" ]]; then
    echo -e "${red}${CMD_NAME} 未安装或安装不完整。请先运行 install.sh${plain}" >&2
    exit 1
fi

if ! command -v systemctl >/dev/null 2>&1; then
    echo -e "${red}本机没有 systemd${plain}" >&2
    exit 1
fi

case $(uname -m) in
    x86_64|x64|amd64) ARCH=amd64 ;;
    aarch64|arm64)    ARCH=arm64 ;;
    armv7l|armv7)     ARCH=armv7 ;;
    s390x)            ARCH=s390x ;;
    *) echo -e "${red}未支持的 CPU 架构:$(uname -m)${plain}" >&2; exit 1 ;;
esac

# ---------- resolve target version ----------

TARGET="${1:-}"
if [[ -z "${TARGET}" ]]; then
    echo -e "${green}查询最新版本…${plain}"
    TARGET=$(curl -fsSL "https://api.github.com/repos/${GH_OWNER}/${GH_REPO}/releases/latest" \
        | grep -E '"tag_name":' \
        | sed -E 's/.*"([^"]+)".*/\1/' || true)
    if [[ -z "${TARGET}" ]]; then
        echo -e "${red}无法获取最新版本(GitHub API 限流?)${plain}" >&2
        exit 1
    fi
fi

CURRENT=$("${INSTALL_DIR}/${CMD_NAME}" -v 2>/dev/null || echo unknown)
echo -e "${green}当前:${plain} ${CURRENT}  ${green}目标:${plain} ${TARGET}  ${green}架构:${plain} ${ARCH}"

if [[ "${TARGET}" == "${CURRENT}" || "${TARGET}" == "v${CURRENT}" ]]; then
    echo -e "${yellow}已经是 ${CURRENT},无需更新(用 \`${CMD_NAME} update ${TARGET}\` 强制重装)${plain}"
    exit 0
fi

# ---------- download ----------

PKG_NAME="nexcore-x-ui-linux-${ARCH}.tar.gz"
URL="https://github.com/${GH_OWNER}/${GH_REPO}/releases/download/${TARGET}/${PKG_NAME}"
SUM_URL="https://github.com/${GH_OWNER}/${GH_REPO}/releases/download/${TARGET}/checksums.txt"
TMP=$(mktemp -d -t nexcore-update.XXXXXX)
trap 'rm -rf "${TMP}"' EXIT

echo -e "${green}下载:${plain} ${URL}"
if ! curl -fSL --connect-timeout 10 -o "${TMP}/pkg.tar.gz" "${URL}"; then
    echo -e "${red}下载失败,请检查 release ${TARGET} 是否存在${plain}" >&2
    exit 1
fi

# ---------- verify SHA256 ----------
# checksums.txt 是 release 必备文件;缺失 / 不匹配则中止,以阻止任何中间人或
# release 资产被替换的攻击。需要 sha256sum(coreutils);busybox 也支持。
echo -e "${green}校验 SHA256…${plain}"
if ! curl -fSL --connect-timeout 10 -o "${TMP}/checksums.txt" "${SUM_URL}"; then
    echo -e "${red}下载 checksums.txt 失败 — release ${TARGET} 缺少校验文件,拒绝继续${plain}" >&2
    exit 1
fi
if ! command -v sha256sum >/dev/null 2>&1; then
    echo -e "${red}本机缺少 sha256sum,无法校验,拒绝继续${plain}" >&2
    exit 1
fi
EXPECTED=$(awk -v want="${PKG_NAME}" '$2 == want || $2 == "*"want {print $1; exit}' "${TMP}/checksums.txt")
if [[ -z "${EXPECTED}" ]]; then
    echo -e "${red}checksums.txt 中找不到 ${PKG_NAME} 的条目${plain}" >&2
    exit 1
fi
ACTUAL=$(sha256sum "${TMP}/pkg.tar.gz" | awk '{print $1}')
if [[ "${EXPECTED}" != "${ACTUAL}" ]]; then
    echo -e "${red}SHA256 不匹配!${plain}" >&2
    echo -e "${red}  expected: ${EXPECTED}${plain}" >&2
    echo -e "${red}  actual:   ${ACTUAL}${plain}" >&2
    exit 1
fi
echo -e "${green}  SHA256 OK: ${ACTUAL}${plain}"

# ---------- extract + sanity ----------

tar -xzf "${TMP}/pkg.tar.gz" -C "${TMP}/"
[[ -d "${TMP}/${CMD_NAME}" ]]              || { echo -e "${red}压缩包结构异常,缺少 ${CMD_NAME}/ 目录${plain}" >&2; exit 1; }
[[ -f "${TMP}/${CMD_NAME}/${CMD_NAME}" ]]  || { echo -e "${red}压缩包缺少二进制 ${CMD_NAME}${plain}" >&2; exit 1; }

# ---------- swap files ----------

echo -e "${green}停止服务…${plain}"
systemctl stop "${SERVICE_NAME}" 2>/dev/null || true

echo -e "${green}替换二进制 + 脚本…${plain}"
install -m 0755 "${TMP}/${CMD_NAME}/${CMD_NAME}"    "${INSTALL_DIR}/${CMD_NAME}"
install -m 0755 "${TMP}/${CMD_NAME}/${CMD_NAME}.sh" "${INSTALL_DIR}/${CMD_NAME}.sh"
install -m 0755 "${INSTALL_DIR}/${CMD_NAME}.sh"     "/usr/bin/${CMD_NAME}"

if [[ -d "${TMP}/${CMD_NAME}/bin" ]]; then
    # 保留运行时 config.json — 业务数据,不属于发行包内容
    keep=""
    if [[ -f "${INSTALL_DIR}/bin/config.json" ]]; then
        keep="${TMP}/config.json.keep"
        cp "${INSTALL_DIR}/bin/config.json" "${keep}"
    fi
    rm -rf "${INSTALL_DIR}/bin"
    cp -a "${TMP}/${CMD_NAME}/bin" "${INSTALL_DIR}/bin"
    # tarball 来自 GitHub Actions runner(uid 1001),解压后 owner 不是 root。
    # binary 端的 preflightBinary() 强校验 xray owner == root,否则拒启,
    # 所以这里强制把整个 install 目录 chown 回 root,避免每次升级都撞这个坑。
    chown -R root:root "${INSTALL_DIR}" 2>/dev/null || true
    chmod +x "${INSTALL_DIR}/bin/"* 2>/dev/null || true
    [[ -n "${keep}" ]] && cp "${keep}" "${INSTALL_DIR}/bin/config.json"
fi

echo -e "${green}启动服务…${plain}"
systemctl start "${SERVICE_NAME}"
sleep 1

NEW=$("${INSTALL_DIR}/${CMD_NAME}" -v 2>/dev/null || echo unknown)
echo
echo -e "${green}═════════════════════════════════════════════${plain}"
echo -e "${green}  升级完成: ${CURRENT} → ${NEW}${plain}"
echo -e "${green}═════════════════════════════════════════════${plain}"
systemctl status "${SERVICE_NAME}" --no-pager --lines=0 | head -8 || true
