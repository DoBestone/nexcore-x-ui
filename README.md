# NexCore x-ui

基于 [vaxilu/x-ui](https://github.com/vaxilu/x-ui) 二开的 xray 节点控制面板,以
**API 优先 + 自动化部署**为目标。适合自部署节点服务器,把它接入业务系统/代理
调度系统作为受控节点;也适合个人单机部署。

> 与原版的差异:Go 1.24、xray-core 1.260+,密码 bcrypt、Cookie 加固、
> 默认凭据全随机、systemd hardening、CI 在线编译、面板内一键登录链接、
> 多 API Token + 调用日志 + 内嵌 API 文档,以及完整的 `/api/v1/*` REST 体系。

---

## 一键安装

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh)
```

指定版本:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/install.sh) v1.0.0
```

安装结束后脚本会**直接打印登录信息**(随机端口 / 随机用户名 / 随机密码),
形如:

```
═════════════════════════════════════════════
  NexCore x-ui 已部署,登录信息如下
═════════════════════════════════════════════
panel port: 38421
username:   admin_aB3xY9
password:   k2Lp9Qr7sW5nMx8t

打开: http://1.2.3.4:38421
```

凭据同时会写到 `/etc/nexcore-x-ui/install-info.txt`(权限 0600),**记下后建议删除**。

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
nexcore-x-ui creds | info          显示 install-info.txt
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
| Server | `GET /api/v1/server/status` |
| xray | `GET /xray/status` · `POST /xray/restart` · `GET /xray/config` · `GET /xray/logs` · `GET\|PUT /xray/template` |
| Inbounds | `GET\|POST /inbounds` (含 `?protocol=&enable=&page=&size=` 过滤) · CRUD by id · `POST /:id/reset-traffic` |
| Inbounds bulk | `POST /inbounds/bulk` · `PATCH /inbounds/bulk-enable` · `POST /inbounds/bulk-delete` · `POST /inbounds/reset-all-traffic` |
| Clients | `/inbounds/:id/clients` CRUD by email |
| Share | `GET /inbounds/:id/links?host=...` · `GET /subscription?host=...` |
| Traffic | `GET /traffic` · `GET /traffic/live` |
| Tokens | `/tokens` CRUD + `/:id/revoke` |
| Magic | `POST /login-tokens` |
| Settings | `GET\|PATCH /settings` · `POST /settings/api-token/rotate` |
| Certs | `GET\|POST /certs` · `DELETE /certs/:name` |
| System | `/system/listening-ports` · `/system/check-port?port=N` · `POST /system/restart-panel` · `GET /system/update-check` · `POST /system/update-apply` |
| Access logs | `GET\|DELETE /access-logs` |

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
`Environment=` 等)、不重装系统依赖、不动 `/etc/nexcore-x-ui/`(数据库 +
`install-info.txt` 完整保留),只:下载 tarball → 停服务 → 替换二进制 +
脚本 + xray bin → 启服务。需要做完整重装请改用 `install.sh`。

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
| `/etc/nexcore-x-ui/install-info.txt` | 首次安装的端口/账号/密码 |
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
