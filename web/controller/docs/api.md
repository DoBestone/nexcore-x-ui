# NexCore x-ui — REST API v1

基础路径:`<面板域名>/api/v1`(默认 `/api/v1`)。除 `/health` 外所有接口都要求携带 Bearer Token。

## 鉴权

Token 在面板首次启动时生成并写入日志(**只显示这一次**):

```
INFO ... API token generated (record now, will not be shown again): <48-char>
```

丢了?调 `POST /api/v1/settings/api-token/rotate` 重新签发一个,旧的会立刻失效。

| 优先级 | 写法 |
|---|---|
| 推荐 | `Authorization: Bearer <token>` |
| 备用 | `X-API-Token: <token>` |
| 最后兜底 | `?api_token=<token>`(不要在 URL 里走生产) |

## 响应外壳

```jsonc
// 成功
{"data": <object | array | null>}

// 失败
{"error": true, "code": "...", "message": "...", "details": ...}
```

---

## 接口总览

```
GET    /health                                   不需要鉴权,健康检查
GET    /server/status                            CPU / 内存 / 流量 / xray 状态

GET    /xray/status                              xray 进程状态 + 当前版本
POST   /xray/restart                             立刻重启 xray
GET    /xray/config                              送给 xray 的最终 JSON
GET    /xray/logs                                xray stdout/stderr 最后 ~100 行
GET    /xray/template                            xray 基础配置模板(字符串)
PUT    /xray/template                            body = 原始 JSON 模板

# 入站
GET    /inbounds                                 ?protocol= &enable= &tag= &search= &page= &size=
GET    /inbounds/:id
POST   /inbounds                                 单条创建
PUT    /inbounds/:id                             整条覆盖更新
PATCH  /inbounds/:id/enable                      body {"enable": bool}
DELETE /inbounds/:id
POST   /inbounds/:id/reset-traffic
POST   /inbounds/bulk                            body = []Inbound,批量创建
PATCH  /inbounds/bulk-enable                     body {"ids":[..],"enable":bool}
POST   /inbounds/bulk-delete                     body {"ids":[..]}
POST   /inbounds/reset-all-traffic               body {"ids":[..]},不传 ids 则重置全部

# 客户端(在 inbound.settings.clients[] 里)
GET    /inbounds/:id/clients
POST   /inbounds/:id/clients                     body = client 对象
PUT    /inbounds/:id/clients/:identifier         body = client 对象
DELETE /inbounds/:id/clients/:identifier

# 分享 / 订阅
GET    /inbounds/:id/links?host=...              JSON 数组,所有 client 的分享 URI
GET    /inbounds/:id/subscription?host=...       text/plain,base64
GET    /subscription?host=...                    text/plain,base64(所有启用入站合并)

# 流量统计
GET    /traffic                                  数据库累计
GET    /traffic/live                             从 xray gRPC 取实时(会清零计数!)

# 设置
GET    /settings
PATCH  /settings
POST   /settings/api-token/rotate

# 多 Token 管理
GET    /tokens
POST   /tokens                                   body {"name": "..."} → token 仅返回一次
PATCH  /tokens/:id                               body {"name": "..."}
POST   /tokens/:id/revoke
DELETE /tokens/:id

# 证书(目录由 NEXCORE_CERT_DIR 决定,默认 /root/cert)
GET    /certs
POST   /certs                                    body {"name","cert","key"}(PEM)
DELETE /certs/:name

# 系统
GET    /system/listening-ports                   gopsutil 看到的所有 TCP-LISTEN/UDP socket
GET    /system/check-port?port=...               {available: bool, owner?}
POST   /system/restart-panel                     SIGHUP 软重启面板
```

---

## 资源细节

### 入站列表过滤参数

| 参数 | 类型 | 含义 |
|---|---|---|
| `protocol` | string | 精确匹配协议名(`vless`、`vmess`...) |
| `enable` | `true`/`false` | 按启用状态过滤 |
| `tag` | string | 精确匹配 tag |
| `search` | string | remark 或 tag 的子串模糊匹配 |
| `page` | int(从 1 开始) | 启用分页 |
| `size` | int | 每页条数,默认 50,上限 200 |

只要传了 `page`,响应壳就变成 `{data: [...], meta: {total, page, size}}`。

### 入站对象(POST/PUT 请求体)

```jsonc
{
  "remark":         "string",
  "enable":         true,
  "expiryTime":     0,                  // unix 毫秒;0 = 不过期
  "total":          0,                  // 字节;0 = 不限流量
  "listen":         "",                 // 留空 = 0.0.0.0(必须是本机 NIC 上的 IP)
  "port":           10086,
  "protocol":       "vless",            // vmess | vless | trojan | shadowsocks | socks | http | Dokodemo-door | wireguard
  "settings":       "<原始 xray JSON>",
  "streamSettings": "<原始 xray JSON>",
  "tag":            "inbound-10086",
  "sniffing":       "<原始 xray JSON>"
}
```

`settings` / `streamSettings` / `sniffing` 三个字段是**字符串**,内容是直接喂给 xray 的 JSON。Reality / XHTTP / Vision 等只要内嵌 JSON 在你跑的 xray-core 版本里合法就能用。

### 各协议 client 对象形状

```jsonc
// VLESS:    {"id": "<UUID>", "flow": "xtls-rprx-vision", "email": "alice"}
// VMess:    {"id": "<UUID>", "alterId": 0,               "email": "alice"}
// Trojan:   {"password": "...",                          "email": "alice"}
// SS-2022:  {"password": "...",                          "email": "alice"}
```

URL 里的 `:identifier` 段不分协议,统一用 `email`。

### 订阅 / 分享链接生成

`?host=` **必传** —— 面板没法可靠推断节点的对外地址(可能是 IP、域名,或者 CDN fronting 域名),由调用方决定。订阅接口返回的是标准 `text/plain` + base64,V2RayN / Shadowrocket 等都能直接订阅。

### Tokens

明文 token 只在创建那一刻返回:

```jsonc
// POST /tokens {"name":"system-A"}
{"data": {"id":1,"name":"system-A","token":"<48 chars>","createdAt":...}}
```

`lastUsedAt` 在每次成功鉴权后异步更新,允许少量延迟。

### 证书

`POST /certs` 在落盘前用 `tls.X509KeyPair` 校验 PEM 对,不合法直接返回 `cert_save_failed`。落盘路径是 `<dir>/<name>.cer` + `<dir>/<name>.key`,权限 0600。`<dir>` 由环境变量 `NEXCORE_CERT_DIR` 决定,默认 `/root/cert`。

### 系统

`POST /system/restart-panel` 给面板自身发 `SIGHUP`,main.go 收到后重载 web server。注意:HTTP 连接会在重载期间断开,调用方必须能容忍这条响应可能没拿到。

---

## 错误码

| 分类 | 错误码 |
|---|---|
| 鉴权 | `missing_api_token` `invalid_api_token` `api_token_not_configured` `auth_db_error` |
| 路径 / 请求体 | `invalid_id` `invalid_body` `invalid_json` `invalid_port` `host_required` |
| 入站 | `inbound_not_found` `create_failed` `update_failed` `delete_failed` `reset_failed` |
| 客户端 | `client_identifier_required` `client_not_found` `client_duplicate` `unsupported_protocol` `client_op_failed` |
| Token | `token_not_found` `revoke_failed` `rename_failed` |
| 证书 | `cert_save_failed` `cert_delete_failed` `cert_list_failed` |
| 订阅 | `subscription_failed` |
| xray | `xray_restart_failed` `xray_traffic_failed` `xray_config_failed` |
| 系统 | `system_query_failed` |
| 后端 | `db_error` `internal_error` `rotate_failed` |

---

## 调用示例

```bash
TOKEN=...
BASE=http://node:54321/api/v1

# 1. 检查 443 端口是否空闲,然后建一个 VLESS+Reality 入站
curl -H "Authorization: Bearer $TOKEN" "$BASE/system/check-port?port=443"

curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds -d '{
    "remark":"node-A","enable":true,"port":443,"protocol":"vless","tag":"inbound-443",
    "settings":"{\"clients\":[{\"id\":\"<UUID>\",\"flow\":\"xtls-rprx-vision\",\"email\":\"alice\"}],\"decryption\":\"none\"}",
    "streamSettings":"{\"network\":\"tcp\",\"security\":\"reality\",\"realitySettings\":{...}}",
    "sniffing":"{\"enabled\":true,\"destOverride\":[\"http\",\"tls\"]}"
  }'

# 2. 在这个入站里追加第二个客户端
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds/1/clients -d '{
    "id":"<UUID2>","flow":"xtls-rprx-vision","email":"bob"
  }'

# 3. 拉订阅,直接给客户端 app
curl "$BASE/subscription?host=cdn.example.com" \
  -H "Authorization: Bearer $TOKEN"

# 4. 给某个业务系统单独发一个 API token
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/tokens -d '{"name":"controller-prod"}'

# 5. 维护期间批量禁用一组入站
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH $BASE/inbounds/bulk-enable -d '{"ids":[1,2,3],"enable":false}'

# 6. 看 xray 实际跑的配置和日志
curl -H "Authorization: Bearer $TOKEN" $BASE/xray/config
curl -H "Authorization: Bearer $TOKEN" $BASE/xray/logs

# 7. 上传 TLS 证书
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/certs -d "{
    \"name\":\"example.com\",
    \"cert\":\"$(awk '{printf "%s\\n",$0}' fullchain.pem)\",
    \"key\":\"$(awk '{printf "%s\\n",$0}' privkey.pem)\"
  }"
```
