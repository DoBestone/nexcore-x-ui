# NexCore x-ui

基于 [vaxilu/x-ui](https://github.com/vaxilu/x-ui) 二开的 xray 节点控制面板,以
**API 优先 + 自动化部署**为目标。适合自部署节点服务器,把它接入业务系统/代理
调度系统作为受控节点;也适合个人单机部署。

> 与原版的差异:Go 1.24、xray-core 1.260+,密码 bcrypt、Cookie 加固、
> 默认凭据全随机、systemd hardening、CI 在线编译、面板内一键登录链接、
> 多 API Token + 调用日志 + 内嵌 API 文档,以及完整的 `/api/v1/*` REST 体系。

---

## 一键安装

**默认装机(不传任何参数 = 常规安装):**

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh)
```

指定版本:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh) v2.6.3
```

安装结束后脚本会**直接打印登录信息**(随机端口 / 随机用户名 / 随机密码 /
**首装 admin-scope API Token**),形如:

```
═════════════════════════════════════════════
  NexCore x-ui 已部署
═════════════════════════════════════════════
  port:           38421
  base path:      /
  secure entry:   (未启用)
  TLS:            no
  username:       admin_aB3xY9
  → http://1.2.3.4:38421/

  首装明文密码 (★ 立即记录,后续无法回查):
    k2Lp9Qr7sW5nMx8t

  首装 API Token (admin scope, ★ 立即记录):
    aB3...XYZ48-char-token
  调用示例: curl -H "Authorization: Bearer <token>" http://1.2.3.4:38421/api/v1/health
```

> **凭据只在 systemd journal 里,不再落盘文件**(v2.1.3+)。错过这次显示就只能
> `nexcore-x-ui creds`(从 journal 重新捞)或 `nexcore-x-ui reset`(强制重新生成)。
> 详见 [自动化部署 / 云厂商一键集成](#自动化部署--云厂商一键集成)。

### 进阶:装机时直接打开"安全入口"

加一个 24 字符随机 slug 把面板钉到 `/<slug>/` 路径下,其它路径**裸 404**(扫端口看不到登录页):

```bash
bash <(curl -fsSL .../install.sh) --secure-entry              # auto 24-char slug
bash <(curl -fsSL .../install.sh) --secure-entry=mySecret123  # 自定 slug
SECURE_ENTRY=1 bash <(curl -fsSL .../install.sh)              # ENV 等价
```

也可以装完后再开:`nexcore-x-ui setting -secureEntry on` (自动生成 slug),或
`nexcore-x-ui setting -secureEntry on -secureEntryPath myCustom`。

支持的 CPU 架构:`amd64` / `arm64` / `armv7` / `s390x`(linux + systemd)。

---

## 管理命令(`nexcore-x-ui` CLI)

按用途分组,大动作前都会确认(`-y` 跳过)。

### 服务控制
```text
nexcore-x-ui                       进入交互菜单
nexcore-x-ui start|stop|restart    启停服务
nexcore-x-ui status                状态摘要
nexcore-x-ui enable|disable        开机自启
nexcore-x-ui log [N]               最近 N 行(默认 200)
nexcore-x-ui log-tail              实时跟踪
```

### 安装生命周期
```text
nexcore-x-ui install [tag]         安装 / 升级
nexcore-x-ui update [tag]          等价于 install
nexcore-x-ui uninstall             卸载(连数据一起删)
```

### 凭据 / 端口
```text
nexcore-x-ui creds | info          从 journal 重新捞首装明文密码 + API token
nexcore-x-ui reset                 重置账号密码 + 端口为新随机值
nexcore-x-ui passwd <user> <pass>  改账号密码
nexcore-x-ui port [N]              查看 / 设置面板端口
```

### 面板访问
```text
nexcore-x-ui url                   打印面板 URL(http://<lan-ip>:<port>)
nexcore-x-ui open                  在本机浏览器打开
nexcore-x-ui magic [ttl=600]       生成一次性 magic-link 登录 URL
```

### 数据
```text
nexcore-x-ui backup [out.tar.gz]   把 /etc/nexcore-x-ui 打成 tarball
nexcore-x-ui restore <in.tar.gz>   从 tarball 恢复
nexcore-x-ui db                    打开 sqlite shell
```

### 健康检查 / 高级
```text
nexcore-x-ui doctor                自检:binary / service / 端口 / /health
nexcore-x-ui version               版本号
nexcore-x-ui setting -<flag>...    binary 内置 setting 子命令
nexcore-x-ui exec <args>           binary 直通
```

全局选项:`-y/--yes` 跳过 confirm · `-q/--quiet` 简化输出。
完整命令清单:`nexcore-x-ui help`。

---

## 面板能力

- **系统状态** — CPU / 内存 / 磁盘 / 网络 / xray 运行态
- **入站列表** — 协议覆盖 VMess / VLESS / Trojan / Shadowsocks / Socks /
  Dokodemo / HTTP / WireGuard,VLESS+Reality、XHTTP、Vision 等高级特性通过
  `streamSettings` 透传(无需面板版本升级)
- **面板设置** — 端口、TLS、xray 模板、电报机器人、时区、**快捷登录链接**
- **API 控制台**(本仓库新增)
  - **API Token 管理**:命名 token,最后使用时间,撤销/删除
  - **调用日志**:每次 `/api/v1/*` 的方法/路径/状态码/耗时/IP/Token 名都
    记录,带过滤 + 分页 + 一键清理 14 天前
  - **API 文档**:从二进制嵌入,在线查看完整 REST 文档

---

## 一键登录链接(magic link)

面板设置 → "快捷登录" → 选有效期(5 分钟 ~ 24 小时)→ 生成。
得到一条形如:

```
https://node.example.com/panel-login/aBc...XYZ
```

**任何浏览过该链接的人都会以当前账号登录**,链接被点击一次或过期即失效。
适用场景:远程协助、CI 跳转面板、内部对接演示。

---

## REST API

完整文档已嵌入面板,登录后进入 `API 控制台 → API 文档` 即可看到。简要导览:

| 资源 | 端点 |
|---|---|
| Liveness | `GET /api/v1/health` |
| Server | `GET /api/v1/server/status`(CPU / 内存 / 磁盘 / **瞬时 B/s** / TCP/UDP 连接数 / xray 状态) |
| xray | `GET /xray/status` · `POST /xray/restart` · `GET /xray/config` · `GET /xray/logs` · `GET\|PUT /xray/template` |
| Inbounds | `GET\|POST /inbounds` (含 `?protocol=&enable=&page=&size=` 过滤) · `GET\|PUT\|DELETE /inbounds/:id` · `PATCH /inbounds/:id/enable` · `POST /:id/reset-traffic` |
| Inbounds bulk | `POST /inbounds/bulk` · `PATCH /inbounds/bulk-enable` · `POST /inbounds/bulk-delete` · `POST /inbounds/reset-all-traffic` · `POST /inbounds/disable-invalid`(扫表禁用过期/超额) |
| Outbounds | `GET\|POST /outbounds` · `GET\|PUT\|DELETE /outbounds/:id` · `PATCH /outbounds/:id/enable` · `POST /outbounds/:id/test`(TCP+TLS 连通测试) · `POST /outbounds/bulk-delete` |
| Block rules | `GET\|POST /block-rules` · `GET\|PUT\|DELETE /block-rules/:id` · `PATCH /block-rules/:id/enable` · `GET /block-rules/presets` · `POST /block-rules/apply-preset` |
| Clients | `/inbounds/:id/clients` CRUD by email · `GET /inbounds/:id/client-traffics` · `GET /clients/:email/traffic` · `POST /clients/:email/reset-traffic` · `PATCH /clients/:email/limits` · `PATCH /clients/:email/enable` · `POST /clients/disable-expired` |
| Share | `GET /inbounds/:id/links?host=...` · `GET /subscription?host=...` · `GET /inbounds/:id/subscription` |
| Traffic | `GET /traffic` · `GET /traffic/live` · `GET /online-ips` · `GET /online-ips-by-email` · `GET /online-ips/:tag` |
| Tokens | `GET\|POST /tokens` · `PATCH /tokens/:id`(rename) · `POST /tokens/:id/revoke` · `DELETE /tokens/:id` |
| Magic | `POST /login-tokens` |
| Settings | `GET\|PATCH /settings` · `POST /settings/api-token/rotate` |
| Certs | `GET\|POST /certs` · `DELETE /certs/:name` |
| System | `/system/listening-ports` · `/system/check-port?port=N` · `POST /system/restart-panel` · `GET /system/update-check` · `POST /system/update-apply` |
| Access logs | `GET\|DELETE /access-logs` |

**Scope 分级**(token 创建时指定):
- `subscription` — 只能调订阅 / 分享链接,适合给客户端订阅 URL
- `readonly` — 加上所有 GET(状态、入站列表、流量等)
- `admin`(默认)— 全部端点,包括 POST/PUT/DELETE/PATCH

完整 schema、请求/响应字段、错误码参见 [docs/API.md](docs/API.md)。

认证:`Authorization: Bearer <token>` / `X-API-Token: <token>` / `?api_token=...`。
Token 通过面板创建(API 控制台)或在节点上 `nexcore-x-ui` 命令行获取。

业务系统接入示例:

```bash
TOKEN=...  ; BASE=http://node:38421/api/v1

# 创建 VLESS+Reality 入站,加 1 个客户端
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds -d '{
    "remark":"node-A","enable":true,"port":443,"protocol":"vless",
    "tag":"inbound-443",
    "settings":"{\"clients\":[{\"id\":\"<UUID>\",\"flow\":\"xtls-rprx-vision\",\"email\":\"alice\"}],\"decryption\":\"none\"}",
    "streamSettings":"{\"network\":\"tcp\",\"security\":\"reality\",\"realitySettings\":{...}}",
    "sniffing":"{\"enabled\":true,\"destOverride\":[\"http\",\"tls\"]}"
  }'

# 取订阅
curl -H "Authorization: Bearer $TOKEN" "$BASE/subscription?host=cdn.example.com"

# 重置流量
curl -H "Authorization: Bearer $TOKEN" -X POST $BASE/inbounds/1/reset-traffic
```

---

## 自动化部署 / 云厂商一键集成

适合给云厂商控制台、Terraform 模块、自建装机调度器接入 — 装机指令传一个
HTTPS webhook + 共享密钥,binary 装完会把**面板地址 / 公网 IP / 端口 / 安全
入口 slug / admin 用户名密码 / API token** 一次性 HMAC-SHA256 签好 POST 出去。

```bash
REPORT_URL=https://provider.example.com/nexcore/cb \
REPORT_KEY=shared_secret_for_hmac \
bash <(curl -fsSL .../install.sh) --secure-entry
```

**Webhook 收到的 JSON**(schemaVersion `"1"`):

```json
{
  "schemaVersion": "1",
  "panelVersion": "2.6.3",
  "timestamp": 1746696000,
  "nonce": "rnd16chars",
  "panel": {
    "port": 38421,
    "scheme": "http",
    "basePath": "/B6h5ikplHPTubXphZq99eUsg/",
    "secureEntryEnabled": true,
    "secureEntryPath": "B6h5ikplHPTubXphZq99eUsg",
    "url": "http://1.2.3.4:38421/B6h5ikplHPTubXphZq99eUsg/",
    "apiBaseURL": "http://1.2.3.4:38421/api/v1",
    "tls": false
  },
  "host": {
    "publicIP": "1.2.3.4",
    "privateIP": "172.18.62.37",
    "hostname": "iZ0xi9c...",
    "arch": "amd64"
  },
  "admin": { "username": "admin_aB3xY9", "password": "k2Lp9Qr7sW5nMx8t" },
  "api":   { "token": "aB3...XYZ48-char-token", "scope": "admin" }
}
```

**Headers:**

```
Content-Type: application/json
X-NexCore-Signature: sha256=<hmac-sha256(body, REPORT_KEY)>
X-NexCore-Timestamp: 1746696000
User-Agent: nexcore-x-ui-installer/2.6.3
```

**接收方校验示例**(Python / FastAPI):

```python
import hmac, hashlib, json, time
@app.post("/cb")
async def cb(req: Request):
    body = await req.body()
    expected = "sha256=" + hmac.new(KEY.encode(), body, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(req.headers["X-NexCore-Signature"], expected):
        raise HTTPException(401, "bad signature")
    payload = json.loads(body)
    if abs(time.time() - payload["timestamp"]) > 300:
        raise HTTPException(401, "stale")  # 防重放
    # ... 入库 / 推给租户控制台 ...
    return Response(status_code=204)
```

**安全模型:**

- **HTTPS 强制**(默认拒 `http://`,`--report-allow-http` 才放行,仅供 LAN 测试)
- **HMAC-SHA256 over body**,共享密钥 `REPORT_KEY` 必须走 ENV(走 CLI 进
  `/proc/<pid>/cmdline` → `ps` 可见 → 同机其他用户能看到密钥)
- **没设 `REPORT_KEY` 直接拒发** — 不允许"裸明文 POST"这种反模式
- **单次 POST、10s 超时、不重试**;失败 install.sh 用 `warn` 不 `die`,凭据
  仍在 journal,运维可手补
- 时间戳 + 16 字符 nonce 都进 body 一起签,接收方按 5 分钟窗口防重放

**完整一行式装机指令(打开安全入口 + 推 webhook):**

```bash
REPORT_URL=https://provider.example.com/cb \
REPORT_KEY=secret \
SECURE_ENTRY=1 \
bash <(curl -fsSL https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh)
```

非首装也能复用:`nexcore-x-ui report -url https://... [-allow-http] [-timeout 10]`,
适合做"实例扩容时把现有节点同步登记到控制面"的运维流程。

---

## 在线更新

面板内调:

```bash
curl -H "Authorization: Bearer $TOKEN" $BASE/system/update-check
curl -H "Authorization: Bearer $TOKEN" -X POST $BASE/system/update-apply
```

也可以命令行(已安装机器):

```bash
nexcore-x-ui update            # 升级到最新 release
nexcore-x-ui update v1.0.0     # 升级 / 降级到指定 tag
```

或用一键脚本(无需先装 CLI,适合自动化 / 远程批量升级):

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/update.sh)
bash <(curl -fsSL https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/update.sh) v1.0.0
```

`update.sh` 与 `install.sh` 的区别:不动 systemd unit(保留你手工加的
`Environment=` 等)、不重装系统依赖、不动 `/etc/nexcore-x-ui/`(数据库
完整保留),只:下载 tarball → 停服务 → 替换二进制 + 脚本 + xray bin →
启服务。需要做完整重装请改用 `install.sh`。

更新流程:从本仓库 GitHub Releases 拉取与本机架构匹配的 tarball → 校验 →
原子替换二进制 → SIGHUP 重启面板。失败会回滚到 `.old` 备份。

> 自更新需要二进制能从 GitHub 拉到。在隔离网络环境可以
> `nexcore-x-ui install <tag>` 手动指定版本,或通过环境变量
> `NEXCORE_GH_OWNER` / `NEXCORE_GH_REPO` 指向自有镜像。

---

## 配置位置

| 路径 | 内容 |
|---|---|
| `/usr/local/nexcore-x-ui/nexcore-x-ui` | 主二进制 |
| `/usr/local/nexcore-x-ui/bin/xray-linux-<arch>` | xray-core 子进程 |
| `/usr/local/nexcore-x-ui/bin/geoip.dat` / `geosite.dat` | 路由数据 |
| `/usr/local/nexcore-x-ui/bin/config.json` | 由面板生成的运行态 xray 配置(0600) |
| `/etc/nexcore-x-ui/nexcore-x-ui.db` | sqlite 数据库 |
| `/etc/systemd/system/nexcore-x-ui.service` | systemd 单元(已加 hardening) |
| `/usr/bin/nexcore-x-ui` | 管理 CLI(指向 `/usr/local/nexcore-x-ui/nexcore-x-ui.sh`) |

> **与原版 vaxilu/x-ui 共存**:本项目所有路径与服务名都用 `nexcore-x-ui`
> 前缀。可以在同一台服务器上同时跑 `x-ui`(原版,`/etc/x-ui` + `x-ui.service`)
> 与 `nexcore-x-ui`(本项目,`/etc/nexcore-x-ui` + `nexcore-x-ui.service`),
> 数据库、systemd unit、CLI 名都不冲突。两套面板需各自占用不同端口。

环境变量:

| 变量 | 默认 | 说明 |
|---|---|---|
| `NEXCORE_DB_PATH` | `/etc/nexcore-x-ui/nexcore-x-ui.db` | 数据库路径 |
| `NEXCORE_DEBUG` | `false` | 调试模式 + 从磁盘热加载模板 |
| `NEXCORE_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `NEXCORE_GH_OWNER` / `NEXCORE_GH_REPO` | `DoBestone` / `nexcore-x-ui` | 自更新源 |
| `NEXCORE_CERT_DIR` | `/root/cert` | 证书目录 |

---

## 开发

```bash
git clone https://github.com/DoBestone/nexcore-x-ui.git
cd nexcore-x-ui
go build -o nexcore-x-ui

# 本地跑(数据库放 /tmp,不污染 /etc)
NEXCORE_DB_PATH=/tmp/nexcore.db ./nexcore-x-ui run
```

热重载:`NEXCORE_DEBUG=true NEXCORE_DB_PATH=/tmp/nexcore.db ./nexcore-x-ui run` —
模板 / 前端资源直接从磁盘读,改完刷新浏览器即可。

测试在线编译:打 tag(`git tag v0.1.0 && git push --tags`)→ GitHub Actions
跨编译 4 个架构(amd64 / arm64 / armv7 / s390x)→ 自动发布 release。

---

## 致谢

Forked from [vaxilu/x-ui](https://github.com/vaxilu/x-ui)。
xray-core 来自 [XTLS](https://github.com/XTLS/Xray-core)。
geoip / geosite 来自 [Loyalsoldier/v2ray-rules-dat](https://github.com/Loyalsoldier/v2ray-rules-dat)。
