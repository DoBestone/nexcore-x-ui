# NexCore x-ui · REST API 参考

完整 `/api/v1/*` 端点参考,以本仓库当前主分支为准。请求 / 响应字段以面板内
"API 控制台 → API 文档"嵌入版本为最权威 — 那份是从二进制里读出来的,跟
binary 永远同步。本文档是给"尚未装上面板"或"想离线对接 SDK"的场景看的。

## 目录

1. [认证](#认证)
2. [Scope 分级](#scope-分级)
3. [响应格式](#响应格式)
4. [端点列表](#端点列表)
   - [Server / xray / Health](#server--xray--health)
   - [Inbounds](#inbounds-入站)
   - [Outbounds](#outbounds-出站)
   - [Block rules](#block-rules-屏蔽规则)
   - [Clients](#clients-客户端)
   - [Share / Subscription](#share--subscription)
   - [Traffic / Online IPs](#traffic--online-ips)
   - [Tokens](#tokens)
   - [Settings / Certs / System](#settings--certs--system)
5. [Webhook 回调(install-time / 事件)](#webhook-回调)

---

## 认证

每个 `/api/v1/*` 请求(除 `/health`)都需要 token,三选一传:

```
Authorization: Bearer <token>
X-API-Token: <token>
?api_token=<token>          # 仅推荐 GET / 订阅链接,会进 access log
```

Token 通过面板"API 控制台 → Tokens"创建,或装机时由 install.sh 一并发的
`first-install` token(scope=admin)。所有 token 在 DB 里只存 SHA256 hash,
plaintext 仅在创建时返回一次。

## Scope 分级

| Scope | 能调的端点 |
|---|---|
| `subscription` | `GET /subscription` · `GET /inbounds/:id/subscription` · `GET /inbounds/:id/links` |
| `readonly` | 上面 + 所有 GET(server/status, inbounds list, traffic, ...) |
| `admin`(默认) | 全部:GET / POST / PUT / DELETE / PATCH |

新建 token 默认 `admin`,这跟 v2.0 之前的"单 token 概念"兼容。订阅 URL 风险高
(常分发给客户端),建议单独发 `subscription` scope token。

## 响应格式

**成功**(2xx):
```json
{ "data": <object|array|null> }
```

**失败**(4xx / 5xx):
```json
{
  "error": true,
  "code": "outbound_not_found",
  "message": "outbound not found",
  "details": null
}
```

`code` 是稳定字符串,业务系统可直接 switch。常见:`invalid_body`、
`invalid_id`、`outbound_not_found`、`tag_duplicate`、`tag_reserved`、
`protocol_unsupported`、`invalid_json`、`xray_config_invalid`、
`protocol_singleton`、`too_many_items`、`db_error`。

---

## 端点列表

### Server / xray / Health

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/health` | (none) | Liveness;不要求 token |
| GET | `/server/status` | readonly | CPU / 内存 / 磁盘 / **瞬时上下行 B/s** / TCP/UDP 连接数 / 累计字节 / xray 状态 / 面板版本 |
| GET | `/xray/status` | readonly | running / version |
| GET | `/xray/config` | readonly | 当前生效的 xray 完整配置 JSON |
| GET | `/xray/logs?kind=access\|error\|all` | readonly | xray 日志。**v2.5.2+ 加 `?kind=`**:`access` = `bin/access.log` 末 100 行(谁在 connect 走哪条 inbound),`error` / `all` / 缺省 = subprocess stdout/stderr 缓冲(xray 自身 startup / 警告 / 错误)。响应里加 `kind` 字段告诉调用方拿到的是哪一路 |
| GET | `/xray/template` | readonly | xray 配置模板(操作员可编辑的部分)。**v2.5.2+** 起改用 `{data: <obj>}` envelope(此前是 raw JSON 不带壳),跟其他端点一致;PUT 仍接收 raw bytes,GET → 改 → PUT 流程 = `JSON.stringify(response.data)` 喂 PUT |
| PUT | `/xray/template` | admin | 写入新模板 → 触发 xray 重启。仍接 raw JSON 字节 |
| POST | `/xray/restart` | admin | 立即重启 xray 子进程 |

`/server/status` 关键字段:`netIO.up` / `netIO.down`(B/s,**两次调用之间的均值**;
首次调用为 0)、`netTraffic.sent` / `netTraffic.recv`(累计字节,业务系统可
自己做差算精确速率)、`tcpCount` / `udpCount`、`uptime`、`xray.{state,version}`。

### Inbounds(入站)

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/inbounds` | readonly | 列表;支持 `?protocol=&enable=&page=&size=&keyword=` |
| GET | `/inbounds/:id` | readonly | 详情 |
| GET | `/inbounds/:id/clients` | readonly | 客户端列表(从 `settings.clients` 解析) |
| POST | `/inbounds` | admin | 新建入站 |
| POST | `/inbounds/bulk` | admin | 批量新建(body: `{"items":[...]}`) |
| PUT | `/inbounds/:id` | admin | 全量更新 |
| PATCH | `/inbounds/:id/enable` | admin | 切开关(body: `{"enable":true}`) |
| PATCH | `/inbounds/bulk-enable` | admin | 批量切(`{"ids":[1,2],"enable":true}`) |
| DELETE | `/inbounds/:id` | admin | 删除 |
| POST | `/inbounds/bulk-delete` | admin | 批量删(`{"ids":[1,2]}`,上限 1000) |
| POST | `/inbounds/:id/reset-traffic` | admin | 单条流量清零 |
| POST | `/inbounds/reset-all-traffic` | admin | 批量清零(`{"ids":[],"all":true}` 才允许全清) |
| POST | `/inbounds/disable-invalid` | admin | **扫表禁用过期 / 超额入站**,返回 `{"affected":N}` |

### Outbounds(出站)

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/outbounds` | readonly | 列表 |
| GET | `/outbounds/:id` | readonly | 详情 |
| POST | `/outbounds` | admin | 新建(`tag` 唯一,不能是 `api/direct/blocked/dns-out`) |
| PUT | `/outbounds/:id` | admin | 更新(**tag 不允许改** — 已有 inbound 引用) |
| PATCH | `/outbounds/:id/enable` | admin | 切开关 |
| DELETE | `/outbounds/:id` | admin | 删除(自动解绑所有引用此 tag 的 inbound) |
| POST | `/outbounds/bulk-delete` | admin | 批量删(事务,自动清 dangling outbound_tag) |
| POST | `/outbounds/:id/test` | admin | TCP dial + 可选 TLS 握手连通测试,返回 `{reachable,latencyMs,tlsOk,tlsError,message}` |

### Block rules(屏蔽规则)

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/block-rules` | readonly | 列表 |
| GET | `/block-rules/:id` | readonly | 详情 |
| GET | `/block-rules/presets` | readonly | 预设列表(广告 / BT / 私有 IP 段 等) |
| POST | `/block-rules` | admin | 新建(type ∈ `domain/ip/geosite/geoip/port/protocol/source`) |
| PUT | `/block-rules/:id` | admin | 更新 |
| PATCH | `/block-rules/:id/enable` | admin | 切开关 |
| DELETE | `/block-rules/:id` | admin | 删除 |
| POST | `/block-rules/apply-preset` | admin | 应用预设(body: `{"key":"ad","inboundTag":"in-443"}`) |

命中流量统一路由到 outbound `blocked`(黑洞)。修改后触发 xray 重启。

### Clients(客户端)

per-inbound:

| Method | Path | Scope | 说明 |
|---|---|---|---|
| POST | `/inbounds/:id/clients` | admin | 增加客户端 |
| PUT | `/inbounds/:id/clients/:identifier` | admin | 更新(identifier = email / id / password 视协议而定) |
| DELETE | `/inbounds/:id/clients/:identifier` | admin | 删除 |
| GET | `/inbounds/:id/client-traffics` | admin | 该入站全部 client 流量行 |

email 全局唯一,以下接口走 email 索引:

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/clients/:email/traffic` | admin | 单个 client 流量 |
| POST | `/clients/:email/reset-traffic` | admin | 流量清零 |
| PATCH | `/clients/:email/limits` | admin | 改 quota / expiry(`{"total":bytes,"expiryTime":epoch_ms}`) |
| PATCH | `/clients/:email/enable` | admin | 切开关 |
| POST | `/clients/disable-expired` | admin | **扫表禁用所有已到期 client**,返回 `{"disabled":[...emails],"count":N}` |

### Share / Subscription

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/inbounds/:id/links?host=...` | subscription | 单入站全客户端的分享链接(vless:// vmess:// 等)。`host` 可省 — 自动按 `?host=` → `settings.nodeAddress` → 请求 Host 兜底 |
| GET | `/inbounds/:id/links/by-email?host=...` | subscription | **v2.5+** 同上但按 email 索引,且每条额外带 `qrcode`(PNG 完整 data URL,可直接 `<img src>`)。返回形如 `{"<email>":{"link":"vless://...","qrcode":"data:image/png;base64,..."}}` |
| GET | `/inbounds/:id/clients/:email/share?host=...` | subscription | **v2.5+** 单个 email 客户端的 `{link, qrcode}` 查询。email 不在该 inbound 时 404 |
| GET | `/inbounds/:id/subscription?host=...` | subscription | 单入站订阅(base64 包) |
| GET | `/subscription?host=...` | subscription | 全部入站合并的订阅 |

### Traffic / Online IPs

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/traffic` | readonly | DB 累计流量(每入站) |
| GET | `/traffic/live?reset=true\|false` | readonly | xray 进程实时流量(`stats` API 抓取)。**v2.5.2+ 加 `?reset=false`** = 只读快照不清零,给外部监控高频轮询用,避免跟 panel `XrayTrafficJob` 抢消费导致 DB 累计漏算。默认 `reset=true` 保持兼容 |
| GET | `/online-ips` | readonly | 当前在线 IP 列表 |
| GET | `/online-ips-by-email?detailed=0\|1` | readonly | 按 email 分组。**v2.5.2+ 加 `?detailed=1`** 返 `{<email>: {ips, inboundTag, lastSeenAt}}`(unix 毫秒);默认 `detailed=0` 保持 `{<email>: ["ip"]}` 兼容形态 |
| GET | `/online-ips/:tag` | readonly | 按 inbound tag 过滤 |

### Tokens

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/tokens` | admin | 列表(只返 hash + 元数据,从不返 plaintext) |
| POST | `/tokens` | admin | 新发(`{"name":"x","scope":"admin","ttl":86400}`,plaintext **只此一次**) |
| PATCH | `/tokens/:id` | admin | 改名 |
| POST | `/tokens/:id/revoke` | admin | 撤销(标记 revoked=true,但保留行) |
| DELETE | `/tokens/:id` | admin | 物理删除 |

### Settings / Certs / System

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/settings` | readonly | 全部面板配置 |
| PATCH | `/settings` | admin | 部分更新 |
| POST | `/settings/api-token/rotate` | admin | 轮换单 token(legacy 模式;多 token 用 `/tokens`) |
| GET | `/certs` | readonly | TLS 证书列表 |
| POST | `/certs` | admin | 上传证书 |
| DELETE | `/certs/:name` | admin | 删除证书 |
| GET | `/system/listening-ports` | readonly | 本机监听端口列表 |
| GET | `/system/check-port?port=N` | readonly | 检查端口是否被占用 |
| POST | `/system/restart-panel` | admin | 自身重启 |
| GET | `/system/update-check` | readonly | 是否有新版本 |
| POST | `/system/update-apply` | admin | 应用升级 |
| POST | `/login-tokens` | admin | 发 magic-link 登录链接(`{"ttl":600,"note":"..."}`) |
| GET | `/access-logs` | readonly | API 调用日志(method/path/status/elapsed/IP/token name) |
| DELETE | `/access-logs` | admin | 清理 14 天前的日志 |

---

## Webhook 回调

### Install-time webhook(`nexcore-x-ui report`)

CLI: `nexcore-x-ui report -url <https-url> [-allow-http] [-timeout 10]`

环境变量:
- `REPORT_KEY` (必填) — HMAC-SHA256 共享密钥
- `NEXCORE_REPORT_PASSWORD` (可选) — 装机首装明文密码,install.sh 自动从 journal 抓
- `NEXCORE_REPORT_API_TOKEN` (可选) — 装机首装 plaintext API token,同上

**Headers:**
```
Content-Type: application/json
X-NexCore-Signature: sha256=<hmac-sha256(body, REPORT_KEY)>
X-NexCore-Timestamp: <unix>
User-Agent: nexcore-x-ui-installer/<version>
```

**Body schema**:见 README 的 [自动化部署 / 云厂商一键集成](../README.md#自动化部署--云厂商一键集成) 章节。

**接收方校验语言版示例:**

<details>
<summary>Python (FastAPI)</summary>

```python
import hmac, hashlib, json, time
from fastapi import HTTPException, Request, Response

KEY = b"shared_secret"

@app.post("/cb")
async def cb(req: Request):
    body = await req.body()
    expected = "sha256=" + hmac.new(KEY, body, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(req.headers["X-NexCore-Signature"], expected):
        raise HTTPException(401, "bad signature")
    payload = json.loads(body)
    if abs(time.time() - payload["timestamp"]) > 300:
        raise HTTPException(401, "stale (replay?)")
    # 入库 / 推给租户
    return Response(status_code=204)
```
</details>

<details>
<summary>Node.js (Express)</summary>

```js
import crypto from "crypto";
import express from "express";

const KEY = "shared_secret";
const app = express();

app.post("/cb", express.raw({ type: "application/json" }), (req, res) => {
  const expected = "sha256=" + crypto.createHmac("sha256", KEY).update(req.body).digest("hex");
  const got = req.headers["x-nexcore-signature"];
  const ok = got && got.length === expected.length &&
             crypto.timingSafeEqual(Buffer.from(got), Buffer.from(expected));
  if (!ok) return res.sendStatus(401);
  const payload = JSON.parse(req.body.toString());
  if (Math.abs(Date.now() / 1000 - payload.timestamp) > 300) return res.sendStatus(401);
  // ...
  res.sendStatus(204);
});
```
</details>

<details>
<summary>Go</summary>

```go
import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "io"
    "net/http"
    "time"
)

var key = []byte("shared_secret")

func cb(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)
    mac := hmac.New(sha256.New, key)
    mac.Write(body)
    expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
    if !hmac.Equal([]byte(r.Header.Get("X-NexCore-Signature")), []byte(expected)) {
        http.Error(w, "bad signature", 401)
        return
    }
    var p struct{ Timestamp int64 `json:"timestamp"` }
    _ = json.Unmarshal(body, &p)
    if abs(time.Now().Unix()-p.Timestamp) > 300 {
        http.Error(w, "stale", 401)
        return
    }
    w.WriteHeader(204)
}

func abs(n int64) int64 { if n < 0 { return -n }; return n }
```
</details>

### 错误处理 / 重试策略

`nexcore-x-ui report` **不重试**:单次 POST 失败 → install.sh 把 webhook 推送
失败用 `warn` 输出,但**安装本身仍然成功**。凭据始终在 systemd journal 里,
运维可用 `nexcore-x-ui creds` 重新捞,或直接 `journalctl -u nexcore-x-ui -n 80
| grep -E 'panel port|username|password|api token'`。

如果接收方暂时挂掉但稍后想拉一份,可以从节点上手跑:
```bash
NEXCORE_REPORT_PASSWORD="$(nexcore-x-ui creds | grep -A1 '密码' | tail -1 | xargs)" \
NEXCORE_REPORT_API_TOKEN="$(nexcore-x-ui creds | grep -A1 'API Token' | tail -1 | xargs)" \
REPORT_KEY=secret \
nexcore-x-ui report -url https://provider.example.com/cb
```

注意非首装场景下密码已 bcrypt 不可逆,通常只能补发 `username` + `api token`。
