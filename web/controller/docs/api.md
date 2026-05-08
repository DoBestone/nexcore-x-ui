# NexCore x-ui — REST API v1

> 接口前缀:`<面板域名>/api/v1` | 请求方式:HTTP/1.1 + JSON | 编码:UTF-8 | 响应:application/json

---

## 概述

面板对外暴露的 HTTP 接口,供业务系统跨节点托管 xray 入站、客户端、流量配额、屏蔽规则、订阅链接、TLS 证书与系统操作。

- **基础路径**:`/api/v1`
- **鉴权**:Bearer Token(`/health` 除外,所有路径都要求)
- **限流**:每个 token(无 token 时按 IP)token-bucket,burst 30、refill 10/s。超出返回 `429 rate_limited`,响应头带 `Retry-After: 1`
- **权限**:每个 token 有作用域(admin / readonly / subscription),决定能调哪些端点

可同时存在多个对接面:
- **业务系统对接**:用 `admin` token 创建/管理入站、客户端、配额(自动化场景的核心)
- **监控/Dashboard**:用 `readonly` token,只能 GET,不能改任何状态
- **终端用户拉订阅**:用 `subscription` token,只能调三条订阅/分享端点
- **运维一次性免密登录**:用 `POST /login-tokens` 签发的 magic-link

---

## 目录

- [鉴权与权限](#鉴权与权限)
- [通用约定](#通用约定)
- [错误码](#错误码)
- [接口总览](#接口总览)
- 资源接口
  - [服务器与健康检查](#服务器与健康检查)
  - [Xray](#xray)
  - [入站(Inbound)](#入站inbound)
  - [入站客户端(Client)](#入站客户端client)
  - [客户端流量与配额](#客户端流量与配额)
  - [在线 IP](#在线-ip)
  - [流量统计](#流量统计)
  - [分享与订阅](#分享与订阅)
  - [屏蔽规则(Block Rule)](#屏蔽规则block-rule)
  - [TLS 证书](#tls-证书)
  - [访问日志](#访问日志)
  - [面板设置](#面板设置)
  - [API Token 管理](#api-token-管理)
  - [Magic-Link 登录](#magic-link-登录)
  - [系统](#系统)
- [跨节点 Webhook(在线 IP 推送)](#跨节点-webhook在线-ip-推送)
- [入站完整画像 / 凭据 / 二维码](#入站完整画像--凭据--二维码)
- [常见问题(FAQ)](#常见问题faq)

---

## 鉴权与权限

### Token 来源

首次启动时面板会自动生成一个 48 字符的 admin token(称为 legacy token)并写入日志,**只显示这一次**:

```
INFO ... API token generated (record now, will not be shown again): <48-char>
```

丢失后调 `POST /api/v1/settings/api-token/rotate` 重新签发,旧 token 立即失效。

除 legacy token 外,面板还支持多 token 表(见 [API Token 管理](#api-token-管理)):数据库只存 SHA256 哈希,明文仅在创建那一刻返回一次,之后任何端点都无法再取到明文。

### Token 传输

| 优先级 | 写法 | 适用场景 |
|---|---|---|
| 推荐 | `Authorization: Bearer <token>` | 标准做法 |
| 备用 | `X-API-Token: <token>` | 反向代理改不了 Authorization 头时 |

URL query 形式 `?api_token=` **已下线**(过去会泄漏到访问日志、浏览器历史、HTTP-aware 代理),迁移期间硬性禁用。

### 权限作用域(Scope)

| Scope | 可访问端点 |
|---|---|
| `admin` | 全部端点(读 + 写 + 敏感操作) |
| `readonly` | 仅观测类 GET(状态、列表、设置查询、日志查询、入站详情、流量等) |
| `subscription` | 仅 `/subscription`、`/inbounds/:id/subscription`、`/inbounds/:id/links` 三条 |

scope 不匹配返回 `403 scope_forbidden`。终端用户场景应单独签发 `subscription` token,把权限面降到最小。

Legacy token 默认是 `admin` scope。

### TTL

`POST /tokens` 的 `ttlSeconds` 字段控制有效时长,`0` = 永不过期。过期 token 在鉴权阶段就被拒绝,不会进入业务逻辑。

### 限流

- 每个 token 一个 token-bucket(无 token 时按客户端 IP)
- burst 30,refill 10 token/s
- 超出限额返回 `429 rate_limited`,带 `Retry-After: 1` 头
- 超过 10 分钟未活动的 bucket 自动 GC,不会无限累积

### 鉴权失败码

| HTTP | code | 触发条件 |
|---|---|---|
| 401 | `missing_api_token` | 没带 token |
| 401 | `invalid_api_token` | token 错误 / 过期 / 已撤销 |
| 401 | `api_token_not_configured` | legacy token 未生成且 multi-token 表为空 |
| 403 | `scope_forbidden` | scope 不允许调用该端点 |
| 429 | `rate_limited` | 超出限流(`Retry-After: 1`) |
| 500 | `auth_db_error` | 鉴权时查库失败 |

---

## 通用约定

### 响应外壳

成功响应统一带 `data` 字段。`null` / 数组 / 对象都合法。

```jsonc
// 200 / 201
{ "data": <object | array | null> }
```

错误响应:

```jsonc
{
  "error":   true,
  "code":    "invalid_body",         // 稳定的机器可读字符串(见错误码表)
  "message": "field 'port' missing", // 人类可读
  "details": { ... }                 // 可选,部分端点带上下文
}
```

`message` 文案可能微调,**不要**用作 switch 依据;请始终基于 `code`。

### HTTP 状态码

| 状态 | 含义 | 何时出现 |
|---|---|---|
| 200 | OK | 普通查询 / 修改成功 |
| 201 | Created | 资源创建成功(`POST /inbounds`、`POST /tokens`、`POST /certs` 等) |
| 204 | No Content | 删除成功(`DELETE /inbounds/:id` 等),响应体为空 |
| 400 | Bad Request | 路径或请求体不合法、业务校验失败 |
| 401 | Unauthorized | 鉴权失败 |
| 403 | Forbidden | scope 不允许 |
| 404 | Not Found | 资源不存在 |
| 429 | Too Many Requests | 触发限流 |
| 500 | Internal Server Error | DB / xray gRPC / 文件系统等后端错误 |

### 时间格式

整个 API 时间字段都是 **unix 毫秒整数**,不用 ISO 8601。

| 字段 | 单位 | 0 的含义 |
|---|---|---|
| `createdAt` / `lastUsedAt` / `expiresAt` / `at` | unix 毫秒 | 未设置 / 永不(具体看上下文) |
| `expiryTime`(入站和 client 配额) | unix 毫秒 | 永不过期 |
| webhook 推送 `ts` | unix 秒 | (单位不同,见 webhook 章节) |

### 字段类型

请求 / 响应里的字段类型严格按 Go struct 定义,不做隐式转换:
- 整数字段(`port`、`total`、`expiryTime`、`ttlSeconds` 等)是 JSON number,**不接受**字符串
- 布尔字段是 JSON `true`/`false`,**不接受**字符串 `"true"` / `"1"`(query string 例外:`?enable=1` / `?enable=true` 都识别)
- xray 配置子段(`settings` / `streamSettings` / `sniffing`)是 JSON 字符串(原样喂给 xray-core),不是嵌套 JSON 对象 —— 对接时要先 `JSON.stringify` 后再放进字段值

---

## 错误码

| 错误码 | HTTP | 说明 | 解决方案 |
|---|---|---|---|
| `missing_api_token` | 401 | 没带 token | 在请求加 `Authorization: Bearer <token>` 或 `X-API-Token` 头 |
| `invalid_api_token` | 401 | token 错误 / 过期 / 已撤销 | 检查 token 是否拼写正确,是否被 `/tokens/:id/revoke` 撤销,是否到达 `expiresAt` |
| `api_token_not_configured` | 401 | legacy token 未生成且 multi-token 表为空 | 调 `POST /settings/api-token/rotate` 生成 legacy token,或在面板创建 multi-token |
| `scope_forbidden` | 403 | scope 不允许 | 用 `admin` scope 调写入端点;readonly token 只能 GET 观测类 |
| `rate_limited` | 429 | 超出限流 | 等 `Retry-After` 秒后重试;高频场景拆分到多个 token |
| `auth_db_error` | 500 | 鉴权时查库失败 | 面板自身故障,查 panel 日志,通常重启即可 |
| `invalid_id` | 400 | URL 路径里 id 不是整数 | 把 `/:id` 段换成数字 |
| `invalid_body` | 400 | JSON body 反序列化失败 | 检查 JSON 语法、字段类型(整数 vs 字符串) |
| `invalid_json` | 400 | `PUT /xray/template` body 不是合法 JSON | xray 模板必须是合法 JSON 串 |
| `invalid_port` | 400 | `?port=` 不是 1..65535 | 传 1..65535 之间的整数 |
| `invalid_tag` | 400 | URL 段 tag 为空 | 检查路径 `/online-ips/:tag` |
| `invalid_key` | 400 | `apply-preset` 没传 `key` | body 必须含非空 `key` |
| `invalid_host` | 400 | `?host=` 校验失败 | host 不能含 CRLF / scheme / 查询串;若设了 `subAllowedHosts` 白名单还要在白名单里 |
| `host_required` | 400 | 订阅 / 分享端点 `?host=` 缺失 | 必传 `?host=节点对外域名` |
| `confirmation_required` | 400 | `reset-all-traffic` 空 body 防呆 | body 传 `{"ids":[..]}` 或显式 `{"all":true}` |
| `too_many_items` | 400 | bulk 端点超过 1000 条 | 拆分成多次调用 |
| `inbound_not_found` | 404 | 入站 id 不存在 | 检查 id;先 `GET /inbounds` 确认 |
| `create_failed` | 400 | 入站创建失败 | 通常是端口冲突、`listen` IP 不在本机、xray 拒绝配置;`details` 有具体原因 |
| `update_failed` | 400 | 入站更新失败 | 同上 |
| `delete_failed` | 500 | 入站删除失败 | DB 错;查面板日志 |
| `reset_failed` | 500 | 流量清零失败 | DB 错 |
| `xray_config_invalid` | 400 | dry-run 整段配置时 xray 拒绝 | 检查 `settings` / `streamSettings` 内嵌 JSON 是否符合你的 xray-core 版本 |
| `protocol_singleton` | 400 | 该协议有单例约束(如 wireguard 入站) | 删掉已有的同协议入站再加 |
| `client_identifier_required` | 400 | URL 段 `:identifier` 为空 | 用 email 作为 identifier |
| `client_not_found` | 400 | 入站里没有该 email/UUID 的 client | 先 `GET /inbounds/:id/clients` 确认 |
| `client_duplicate` | 400 | email/UUID 重复 | 同入站内 email 必须唯一,xray 自身约束 |
| `unsupported_protocol` | 400 | 不支持该协议的 client 操作 | 检查入站协议,只支持 vmess / vless / trojan / shadowsocks |
| `client_op_failed` | 400 | client 操作通用失败 | `details` 有具体原因 |
| `client_traffic_not_found` | 404 | `client_traffics` 表里没有该 email | 该 email 从未有过流量记录,或入站已删 |
| `token_not_found` | 404 | token id 不存在 | `GET /tokens` 确认 |
| `revoke_failed` | 500 | token 撤销失败 | DB 错 |
| `rename_failed` | 400 | token 改名失败 | 通常是 name 重复或非法字符 |
| `magic_token_failed` | 500 | magic-link 创建失败 | DB 错 |
| `cert_save_failed` | 400 | 证书 PEM 校验或落盘失败 | `tls.X509KeyPair` 不通过,或 `NEXCORE_CERT_DIR` 没写权限 |
| `cert_delete_failed` | 400 | 证书删除失败 | 文件不存在或目录无写权限 |
| `cert_list_failed` | 500 | 列证书失败 | `NEXCORE_CERT_DIR` 无读权限 |
| `toggle_failed` | 400 | block-rule enable/disable 失败 | DB 错或 id 不存在 |
| `apply_failed` | 400 | block-rule preset 应用失败 | `details` 有具体原因 |
| `subscription_failed` | 500 | 订阅生成失败 | 入站为空、所有 client 配置异常等 |
| `xray_restart_failed` | 500 | xray 重启失败 | 看 `/xray/status` 的 `error` / `lastOutput` |
| `xray_traffic_failed` | 500 | xray gRPC stats 拉取失败 | xray 没起、stats 没启用、gRPC 端口不通 |
| `xray_config_failed` | 500 | 取 xray 当前配置失败 | xray 没启或没生成过配置 |
| `system_query_failed` | 500 | gopsutil 查询失败 | 通常是面板权限不足 / 系统不支持 |
| `update_check_failed` | 500 | 自更新版本探测失败 | 网络不通 GitHub |
| `update_apply_failed` | 500 | 自更新执行失败 | 磁盘空间 / 权限 / 网络 |
| `purge_failed` | 500 | 日志清理失败 | DB 错 |
| `db_error` | 500 | 通用 DB 错误 | 查面板日志 |
| `internal_error` | 500 | 通用后端错误 | 查面板日志 |
| `rotate_failed` | 500 | legacy token 重签失败 | DB 错 |

---

## 接口总览

> 列里 `[Scope]` 代表最低 scope 要求。`admin` 端点只能 admin 调,`readonly` 端点 admin / readonly 都行,`subscription` 端点 admin / readonly / subscription 都行。

| Method | Path | Scope | 说明 |
|---|---|---|---|
| GET | `/health` | (无) | 健康探测 |
| GET | `/server/status` | readonly | CPU / 内存 / 网卡 / xray 综合状态 |
| GET | `/xray/status` | readonly | xray 进程状态 + 版本 |
| GET | `/xray/config` | readonly | xray 当前最终 JSON |
| GET | `/xray/logs` | readonly | xray stdout/stderr 最近 ~100 行 |
| GET | `/xray/template` | readonly | xray 基础模板 |
| PUT | `/xray/template` | admin | 整段覆盖模板 |
| POST | `/xray/restart` | admin | 立即重启 xray |
| GET | `/inbounds` | readonly | 入站列表(支持过滤、分页、字段裁剪) |
| GET | `/inbounds/:id` | readonly | 单入站完整对象 |
| POST | `/inbounds` | admin | 创建入站 |
| PUT | `/inbounds/:id` | admin | 整条覆盖入站 |
| PATCH | `/inbounds/:id/enable` | admin | 启停入站 |
| DELETE | `/inbounds/:id` | admin | 删除入站 |
| POST | `/inbounds/:id/reset-traffic` | admin | 入站累计流量清零 |
| POST | `/inbounds/bulk` | admin | 批量创建入站(≤1000) |
| PATCH | `/inbounds/bulk-enable` | admin | 批量启停入站 |
| POST | `/inbounds/bulk-delete` | admin | 批量删除入站 |
| POST | `/inbounds/reset-all-traffic` | admin | 批量 / 全量清零流量 |
| GET | `/inbounds/:id/clients` | readonly | 入站客户端列表 |
| POST | `/inbounds/:id/clients` | admin | 添加客户端 |
| PUT | `/inbounds/:id/clients/:identifier` | admin | 整条覆盖客户端 |
| DELETE | `/inbounds/:id/clients/:identifier` | admin | 删除客户端 |
| GET | `/inbounds/:id/client-traffics` | admin | 入站下所有 client 的流量 / 配额 |
| GET | `/clients/:email/traffic` | admin | 单个 email 的流量记录 |
| POST | `/clients/:email/reset-traffic` | admin | 清零该 email 的 up/down |
| PATCH | `/clients/:email/limits` | admin | 修改 total / expiryTime / enable |
| PATCH | `/clients/:email/enable` | admin | 启停 client(轻量端点,只看 enable) |
| POST | `/clients/disable-expired` | admin | 批量禁用所有到期 client |
| GET | `/online-ips` | readonly | 全量在线 IP `{tag: [ip,..]}` |
| GET | `/online-ips/:tag` | readonly | 单入站在线 IP |
| GET | `/online-ips-by-email` | readonly | 按 client 维度的在线 IP |
| GET | `/traffic` | readonly | 数据库累计流量(便宜) |
| GET | `/traffic/live` | readonly | 实时 xray gRPC stats(**会清零计数**) |
| GET | `/inbounds/:id/links` | subscription | 单入站所有 client 的分享 URI 数组 |
| GET | `/inbounds/:id/subscription` | subscription | 单入站的 base64 订阅 |
| GET | `/subscription` | subscription | 全部启用入站合并的 base64 订阅 |
| GET | `/block-rules` | readonly | 屏蔽规则列表 |
| GET | `/block-rules/presets` | readonly | 内置预设 |
| POST | `/block-rules` | admin | 创建规则 |
| PUT | `/block-rules/:id` | admin | 整条覆盖 |
| PATCH | `/block-rules/:id/enable` | admin | 启停规则 |
| DELETE | `/block-rules/:id` | admin | 删除规则 |
| POST | `/block-rules/apply-preset` | admin | 应用预设 |
| GET | `/certs` | readonly | 证书列表 |
| POST | `/certs` | admin | 上传证书 |
| DELETE | `/certs/:name` | admin | 删除证书 |
| GET | `/access-logs` | readonly | API 访问日志查询 |
| DELETE | `/access-logs` | admin | 清空访问日志 |
| GET | `/settings` | readonly | 全部面板设置 |
| PATCH | `/settings` | admin | 部分更新设置 |
| POST | `/settings/api-token/rotate` | admin | 重签 legacy token |
| GET | `/tokens` | readonly | API token 列表 |
| POST | `/tokens` | admin | 创建 API token |
| PATCH | `/tokens/:id` | admin | 改名 |
| POST | `/tokens/:id/revoke` | admin | 撤销 |
| DELETE | `/tokens/:id` | admin | 删除 |
| POST | `/login-tokens` | admin | 签发 magic-link |
| GET | `/system/listening-ports` | readonly | 本机所有 LISTEN socket |
| GET | `/system/check-port` | readonly | 端口占用查询 |
| GET | `/system/update-check` | readonly | 自更新版本检测 |
| POST | `/system/update-apply` | admin | 触发自更新 |
| POST | `/system/restart-panel` | admin | SIGHUP 软重启面板 |

---

## 服务器与健康检查

### `GET /health`

液体探测,**不需要鉴权**。可用于负载均衡、业务系统在 token 配置前的可达性检测。

无参数。

**请求示例:**

```bash
curl http://node:54321/api/v1/health
```

**响应示例:**

```json
{
  "data": {
    "status": "ok",
    "time":   "2026-05-07T03:20:00Z"
  }
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| status | string | 固定 `"ok"` |
| time | string | 服务器当前时间(RFC 3339) |

---

### `GET /server/status`

CPU / 内存 / 磁盘 / 网卡 / xray 进程的综合状态。响应较大,推荐 ≥5s 轮询。

无参数。

**响应示例:**

```jsonc
{
  "data": {
    "cpu":      { "percent": 12.4, "cores": 8 },
    "mem":      { "current": 1234567890, "total": 8589934592 },
    "swap":     { "current": 0, "total": 0 },
    "disk":     { "current": 12345678901, "total": 53687091200 },
    "xray":     { "state": "running", "errorMsg": "", "version": "1.8.4" },
    "uptime":   12345,
    "loads":    [0.12, 0.34, 0.56],
    "tcpCount": 23,
    "udpCount": 7,
    "netIO":    { "up": 1234, "down": 5678 },
    "netTraffic": { "sent": 1234567890, "recv": 9876543210 },
    "publicIP": { "ipv4": "...", "ipv6": "..." },
    "appStats": { "threads": 14, "mem": 23456789, "uptime": 12345 }
  }
}
```

字段类型与单位:
| 字段 | 类型 | 单位 |
|---|---|---|
| cpu.percent | float | 百分比(0~100) |
| cpu.cores | int | 逻辑核数 |
| mem / swap / disk current/total | int64 | 字节 |
| xray.state | string | `running` / `stop` / `error` |
| uptime | int64 | 秒 |
| loads | float[3] | Linux load 1m/5m/15m |
| netIO up/down | int64 | 字节/秒(瞬时) |
| netTraffic sent/recv | int64 | 字节(自启动累计) |

---

## Xray

### `GET /xray/status`

xray 进程状态 + 版本。异常时附带 stderr 末尾摘要。

无参数。

**响应示例(正常):**

```json
{ "data": { "running": true, "version": "1.8.4" } }
```

**响应示例(异常):**

```json
{
  "data": {
    "running":    false,
    "version":    "1.8.4",
    "error":      "exit status 1",
    "lastOutput": "[Fatal] failed to parse config: ..."
  }
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| running | bool | 进程是否在运行 |
| version | string | xray-core 版本号 |
| error | string | 异常时附带,通常是 exit status |
| lastOutput | string | xray stderr 最末几百字符 |

---

### `GET /xray/config`

xray 当前实际加载的最终 JSON(模板 + 入站合并后的结果)。响应体可能很大。

无参数。响应 `Content-Type: application/json`,直接是配置 JSON,**不**包裹 `{data: ...}` 外壳。

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" $BASE/xray/config
```

---

### `GET /xray/logs`

xray stdout/stderr 最近 ~100 行。

无参数。

**响应示例:**

```jsonc
{
  "data": {
    "logs": ["2026/05/07 03:20:00 [Info] ...", "..."]
  }
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| logs | string[] | 最末若干行,旧 → 新 |

---

### `GET /xray/template`

xray 基础配置模板(原始 JSON 字符串)。响应 `Content-Type: application/json`,直接是 JSON,**不**包裹 data 外壳。

无参数。

---

### `PUT /xray/template`

整段覆盖模板。落库前会 `json.Unmarshal` 校验,失败返回 `invalid_json`。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| (raw body) | 是 | JSON | 完整模板内容,直接作为请求体,不要包裹 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PUT $BASE/xray/template -d @template.json
```

**响应示例:**

```json
{ "data": { "updated": true } }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| updated | bool | 是否落库成功(成功后会自动标 xray needsRestart) |

---

### `POST /xray/restart`

立即重启 xray。

无参数。

**响应示例:**

```json
{ "data": { "restarted": true } }
```

---

## 入站(Inbound)

### Inbound 对象

| 字段 | 类型 | 说明 |
|---|---|---|
| id | int | 系统主键(响应里出现,创建时不传) |
| remark | string | 备注 |
| enable | bool | 是否启用 |
| expiryTime | int64 | unix 毫秒;0 = 不过期 |
| total | int64 | 字节;0 = 不限流量 |
| listen | string | 留空 = 0.0.0.0;非空必须是本机 NIC 上的 IP |
| port | int | 监听端口 |
| protocol | string | `vmess` / `vless` / `trojan` / `shadowsocks` / `socks` / `http` / `dokodemo-door` / `wireguard` |
| settings | string | 内嵌 xray JSON 字符串(client 列表、加密方式等) |
| streamSettings | string | 内嵌 xray JSON 字符串(network、tls、reality 等) |
| sniffing | string | 内嵌 xray JSON 字符串 |
| tag | string | 入站 tag,xray 路由用 |
| up / down | int64 | 累计流量(字节);响应里出现 |

> **重要**:`settings` / `streamSettings` / `sniffing` 是字符串,内容是直接喂给 xray-core 的 JSON。对接时先把对象 `JSON.stringify` 成字符串再放进字段。

### `GET /inbounds`

入站列表,默认裁剪掉 `settings` / `streamSettings` / `sniffing` 三个大字段(每条几 KB),100 条入站从 200KB 降到 ~20KB。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| protocol | 否 | string | 精确匹配协议名 |
| enable | 否 | string | `true` / `false` / `1` / `0` |
| tag | 否 | string | 精确匹配 tag |
| search | 否 | string | remark 或 tag 子串模糊 |
| page | 否 | int | 从 1 开始;传了才启用分页 |
| size | 否 | int | 每页条数,默认 50 |
| full | 否 | string | `1` 时返回完整对象 |
| include | 否 | string | 含 `settings` 时等价 `full=1` |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "$BASE/inbounds?protocol=vless&enable=true&page=1&size=20"
```

**响应示例(默认裁剪 + 分页):**

```jsonc
{
  "data": [
    {
      "id":          3,
      "port":        443,
      "protocol":    "vless",
      "tag":         "inbound-443",
      "remark":      "node-A",
      "enable":      true,
      "listen":      "",
      "up":          123456789,
      "down":        987654321,
      "total":       0,
      "expiryTime":  0,
      "outboundTag": ""
    }
  ],
  "meta": { "total": 12, "page": 1, "size": 20, "full": false }
}
```

> `outboundTag` **v2.5.1+** 进入裁剪视图。空串 = 直连(走 freedom);非空 = 该入站流量被路由到对应 Outbound.tag。业务系统拉列表做"哪条入站走哪个出口"对账时直接读这个字段,不必 `?full=1` 拉巨大的 settings 字节。

| 响应字段 | 类型 | 说明 |
|---|---|---|
| data | object[] | 入站对象数组(裁剪或完整,看 `full`) |
| meta.total | int | 满足过滤条件的总数 |
| meta.page / size | int | 当前页与每页条数 |
| meta.full | bool | 当次响应是否含完整字段 |

> 不传 `page` 时不返回 `meta`,直接 `{"data": [...]}`。

---

### `GET /inbounds/:id`

返回单入站完整对象(永远含 settings / streamSettings / sniffing)。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |

**响应示例:**

```jsonc
{
  "data": {
    "id":             3,
    "userId":         1,
    "up":             123,
    "down":           456,
    "total":          0,
    "remark":         "node-A",
    "enable":         true,
    "expiryTime":     0,
    "listen":         "",
    "port":           443,
    "protocol":       "vless",
    "settings":       "{\"clients\":[...],\"decryption\":\"none\"}",
    "streamSettings": "{\"network\":\"tcp\",...}",
    "tag":            "inbound-443",
    "sniffing":       "{\"enabled\":true,...}"
  }
}
```

字段语义见 [Inbound 对象](#inbound-对象)。

---

### `POST /inbounds`

创建入站。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| remark | 否 | string | 备注 |
| enable | 否 | bool | 默认 false |
| expiryTime | 否 | int64 | unix 毫秒,0 = 不过期 |
| total | 否 | int64 | 字节,0 = 不限 |
| listen | 否 | string | 留空 = 0.0.0.0,必须是本机 NIC 上的 IP |
| port | 是 | int | 监听端口 |
| protocol | 是 | string | 协议名 |
| settings | 是 | string | 内嵌 xray JSON 字符串 |
| streamSettings | 否 | string | 内嵌 xray JSON 字符串 |
| sniffing | 否 | string | 内嵌 xray JSON 字符串 |
| tag | 是 | string | 入站 tag |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds -d '{
    "remark":"node-A","enable":true,"port":443,
    "protocol":"vless","tag":"inbound-443",
    "settings":"{\"clients\":[{\"id\":\"<UUID>\",\"flow\":\"xtls-rprx-vision\",\"email\":\"alice\"}],\"decryption\":\"none\"}",
    "streamSettings":"{\"network\":\"tcp\",\"security\":\"reality\",\"realitySettings\":{...}}",
    "sniffing":"{\"enabled\":true,\"destOverride\":[\"http\",\"tls\"]}"
  }'
```

**响应示例:**

```jsonc
{
  "data": {
    "id": 3,
    "port": 443,
    "protocol": "vless",
    /* ...其余字段同 GET /inbounds/:id */
  }
}
```

字段见 [Inbound 对象](#inbound-对象)。

错误码:`invalid_body` / `xray_config_invalid` / `protocol_singleton` / `create_failed`。

---

### `PUT /inbounds/:id`

整条覆盖,**不是部分更新**:body 里没传的字段会按零值覆盖。要保留现有数据应先 `GET /inbounds/:id`,改完整对象后再 PUT。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| (body) | 是 | object | Inbound 对象,字段同 POST /inbounds |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PUT $BASE/inbounds/3 -d @inbound.json
```

**响应示例:** 同 `GET /inbounds/:id`。

---

### `PATCH /inbounds/:id/enable`

启停入站(轻量端点,不需要传整个对象)。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| enable | 是 | bool | true/false |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH $BASE/inbounds/3/enable -d '{"enable": false}'
```

**响应示例:** 整个 Inbound 对象(更新后状态)。

---

### `DELETE /inbounds/:id`

删除入站。**不会**级联删除 `client_traffics` 表里历史 email 的流量记录(为保留审计)。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -X DELETE $BASE/inbounds/3
```

**响应:** HTTP 204,无 body。

---

### `POST /inbounds/:id/reset-traffic`

把入站累计 up/down 清零(不影响 client 级流量)。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |

**响应示例:** 整个 Inbound 对象(up/down 已清零)。

---

### `POST /inbounds/bulk`

批量创建入站,**单事务,要么全成要么全失败**。最多 1000 条。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| (raw body) | 是 | object[] | Inbound 对象数组,每条字段同 POST /inbounds |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds/bulk -d '[{"port":10086,...}, {"port":10087,...}]'
```

**响应示例:**

```json
{ "data": { "created": 2, "items": [ /* 完整 Inbound 对象 */ ] } }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| created | int | 创建数量 |
| items | object[] | 创建后的完整 Inbound 对象数组 |

---

### `PATCH /inbounds/bulk-enable`

批量启停。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| ids | 是 | int[] | 入站 id 数组,最多 1000 |
| enable | 是 | bool | true/false |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH $BASE/inbounds/bulk-enable -d '{"ids":[1,2,3],"enable":false}'
```

**响应示例:**

```json
{ "data": { "affected": 3 } }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| affected | int | 实际改动行数 |

---

### `POST /inbounds/bulk-delete`

批量删除。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| ids | 是 | int[] | 入站 id 数组,最多 1000 |

**响应示例:**

```json
{ "data": { "affected": 3 } }
```

---

### `POST /inbounds/reset-all-traffic`

批量 / 全量清零入站累计流量。**防呆**:body 必须显式指定范围,空 body 返回 `confirmation_required`。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| ids | 二选一 | int[] | 指定 id 列表 |
| all | 二选一 | bool | `true` 表示重置所有入站 |

**请求示例(指定 id):**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds/reset-all-traffic -d '{"ids":[1,2,3]}'
```

**请求示例(全量):**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds/reset-all-traffic -d '{"all": true}'
```

**响应示例:**

```json
{ "data": { "affected": 12 } }
```

---

## 入站客户端(Client)

### Client 对象(各协议)

| 协议 | 必填字段 | 说明 |
|---|---|---|
| VLESS | `id`(UUID), `flow`, `email` | flow 通常 `xtls-rprx-vision` 或留空 |
| VMess | `id`(UUID), `alterId`, `email` | alterId 通常 0 |
| Trojan | `password`, `email` | |
| Shadowsocks (含 SS-2022) | `password`, `email` | SS-2022 password 是 base64 |

URL 段 `:identifier` 不分协议,**统一用 email**。

> `PUT` 是整条覆盖 —— body 必须包含该协议要求的所有字段。

### `GET /inbounds/:id/clients`

列出入站当前 `inbound.settings.clients[]` 数组。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |

**响应示例(VLESS 入站):**

```json
{
  "data": [
    { "id": "uuid-1...", "flow": "xtls-rprx-vision", "email": "alice" },
    { "id": "uuid-2...", "flow": "xtls-rprx-vision", "email": "bob" }
  ]
}
```

字段含义见 [Client 对象](#client-对象各协议)。

---

### `POST /inbounds/:id/clients`

向入站追加一个客户端。会触发 xray reload。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| (body) | 是 | object | 见 [Client 对象](#client-对象各协议) |

**请求示例(VLESS):**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds/3/clients -d '{
    "id":"<UUID>","flow":"xtls-rprx-vision","email":"bob"
  }'
```

**响应示例:** 完整 Inbound 对象(含新增 client 后的 settings)。

错误码:`client_duplicate` / `unsupported_protocol` / `client_op_failed`。

---

### `PUT /inbounds/:id/clients/:identifier`

整条覆盖某 client。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| identifier | 是 | string | URL 路径段,client 的 email |
| (body) | 是 | object | 完整 Client 对象 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PUT $BASE/inbounds/3/clients/alice -d '{
    "id":"<新UUID>","flow":"xtls-rprx-vision","email":"alice"
  }'
```

**响应示例:** 完整 Inbound 对象。

错误码:`client_not_found` / `client_op_failed`。

---

### `DELETE /inbounds/:id/clients/:identifier`

删除某 client。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| identifier | 是 | string | URL 路径段,email |

**响应:** HTTP 204,无 body。

---

## 客户端流量与配额

panel 在 `client_traffics` 表里按 email 维度跟踪流量、上限、到期、启用 —— 这跟入站层的 `Inbound.total` / `Inbound.expiryTime` 是两套独立维度,前者控**单个 client**,后者控**整个入站**。

### ClientTraffic 对象

| 字段 | 类型 | 说明 |
|---|---|---|
| id | int | 主键 |
| inboundId | int | 所属入站 |
| email | string | 全局唯一 |
| up | int64 | 累计上行字节 |
| down | int64 | 累计下行字节 |
| total | int64 | 流量上限(字节);0 = 不限 |
| expiryTime | int64 | unix 毫秒;0 = 不过期 |
| enable | bool | 是否启用(到期/触顶后自动写 false) |

#### 自动 disable

xray gRPC stats job 拉到流量后会顺手判断:`(up+down) >= total > 0` 或 `now >= expiryTime > 0` 就自动写 `enable=false`,然后触发 xray reload 把 client 踢下线。`POST /clients/disable-expired` 是显式版本(下面有说明)。

---

### `GET /inbounds/:id/client-traffics`

入站下所有 client 的流量行。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |

**响应示例:**

```json
{
  "data": [
    { "id": 1, "inboundId": 3, "email": "alice",
      "up": 1234, "down": 5678, "total": 0, "expiryTime": 0, "enable": true }
  ]
}
```

---

### `GET /clients/:email/traffic`

单个 email 的流量行。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| email | 是 | string | URL 路径段 |

**响应示例:** 单个 ClientTraffic 对象,字段同上。

错误码:`client_traffic_not_found`(404)。

---

### `POST /clients/:email/reset-traffic`

清零该 email 的 up / down(不动 total / expiryTime / enable)。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| email | 是 | string | URL 路径段 |

**响应示例:**

```json
{ "data": { "reset": true, "email": "alice" } }
```

---

### `PATCH /clients/:email/limits`

修改流量上限 / 到期 / 启用状态。**只更新传了的字段**,不传 = 不改。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| email | 是 | string | URL 路径段 |
| total | 否 | int64 | 字节;0 = 不限,非零是字节上限 |
| expiryTime | 否 | int64 | unix 毫秒;0 = 不过期,非零是绝对时间 |
| enable | 否 | bool | false 立即踢下线(下次 xray reload 后生效) |

**请求示例:**

```bash
# 设流量上限 10 GiB,30 天后到期
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH $BASE/clients/alice/limits -d '{
    "total": 10737418240,
    "expiryTime": '"$(($(date +%s)+30*86400))"'000
  }'

# 仅禁用
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH $BASE/clients/alice/limits -d '{"enable": false}'
```

**响应示例:** 完整 ClientTraffic 对象(更新后状态)。

每次调用都会触发 xray dirty 标记 → 下次心跳自动 reload。

---

### `PATCH /clients/:email/enable`

启停单个 client 的轻量端点(对称 `PATCH /inbounds/:id/enable`)。功能上等价于 `PATCH /clients/:email/limits` 只传 `enable` 字段,但语义更清晰、调用方不需要知道 `SetLimitsParams` 形状。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| email | 是 | string | URL 路径段 |
| enable | 是 | bool | true = 启用,false = 立即禁用并踢下线 |

**请求示例(暂停):**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH $BASE/clients/alice/enable -d '{"enable": false}'
```

**请求示例(恢复):**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH $BASE/clients/alice/enable -d '{"enable": true}'
```

**响应示例:**

```json
{ "data": { "email": "alice", "enable": false } }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| email | string | 操作的 email,原样返回 |
| enable | bool | 操作后的状态 |

调用后会触发 xray dirty 标记 → 下次心跳自动 reload。`false` 状态下 xray 把该 email 从 user list 移除,已建立的连接也会被踢断。

错误码:`client_traffic_not_found`(该 email 在 `client_traffics` 表里没记录,需要先创建 client 让 xray 跑起来后该表才会有行)。

---

### `POST /clients/disable-expired`

扫描所有到期但仍启用的 client,批量置 `enable=false` 并触发 xray reload。业务系统月初对账可手动调一次。

无参数。

**响应示例:**

```json
{ "data": { "disabled": ["alice", "bob"], "count": 2 } }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| disabled | string[] | 本次被禁用的 email 列表 |
| count | int | 数量 |

---

## 在线 IP

panel 60s 周期解析 xray access.log,把 `(email/tag, sourceIP)` 在内存维护滑动窗口,**面板重启即清**。

> best-effort 数据:access.log 关闭、轮转、xray 没启日志的场景下会拿到空数组;只反映"最近 60s 内有过流量"的源 IP,不是 LIVE 长连接计数。

### `GET /online-ips`

全量,按入站 tag 分组。

无参数。

**响应示例:**

```json
{
  "data": {
    "inbound-443":   ["1.2.3.4", "5.6.7.8"],
    "inbound-10086": ["9.10.11.12"]
  }
}
```

| 响应 | 类型 | 说明 |
|---|---|---|
| data | `map[string][]string` | tag → IP 列表 |

---

### `GET /online-ips/:tag`

单入站。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| tag | 是 | string | URL 路径段,入站 tag |

**响应示例:**

```json
{ "data": ["1.2.3.4", "5.6.7.8"] }
```

---

### `GET /online-ips-by-email`

按 client 维度。判定某个 client 是否在线、来自哪些 IP 时直接查这条。

无参数。

**响应示例:**

```json
{
  "data": {
    "alice": ["1.2.3.4"],
    "bob":   []
  }
}
```

| 响应 | 类型 | 说明 |
|---|---|---|
| data | `map[string][]string` | email → IP 列表 |

---

## 流量统计

### `GET /traffic`

数据库累计流量(便宜,可高频轮询)。

无参数。

**响应示例:**

```json
{
  "data": [
    { "id": 3, "tag": "inbound-443", "port": 443,
      "up": 1234567, "down": 9876543, "total": 0, "enable": true }
  ]
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| id / tag / port / total / enable | | 同 Inbound 对应字段 |
| up / down | int64 | 累计字节 |

---

### `GET /traffic/live`

实时拉 xray gRPC stats(`reset=true`),**会清零 xray 内部计数**,只能由 panel 心跳调,业务系统避免直接调。

> /traffic/live 把数据交回 panel 后,panel 会把增量累加进 DB。所以高频外部调用会让 panel 自己统计漏算。**只用于诊断**。

无参数。

**响应示例:**

```json
{
  "data": [
    { "type": "inbound", "name": "inbound-443", "up": 1234, "down": 5678 },
    { "type": "user",    "name": "alice",      "up": 999,  "down": 888 }
  ]
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| type | string | `inbound` / `user`(后者按 email 聚合) |
| name | string | 入站 tag 或 email |
| up / down | int64 | 本次窗口内字节 |

---

## 分享与订阅

`?host=` **必传**且经过语法校验(屏蔽 CRLF / scheme / 查询串 / 长度炸弹)。

如果在 `/settings` 配了 `subAllowedHosts`(逗号分隔白名单),host 必须在白名单里,否则 `400 invalid_host`。这是为了在 token 泄漏后限制攻击者用 token 生成指向 attacker.example 的"看起来合法"的订阅链接。

订阅响应是标准 `text/plain` + base64,V2RayN / Shadowrocket / Clash 等客户端可直接订阅。

### `GET /inbounds/:id/links`

单入站所有 client 的分享 URI 数组(顺序与 `/clients` 列表对应)。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| host | 否 | string | 节点对外域名/IP,query 参数。**v2.5+** 起可省 — 自动 `?host=` → `settings.nodeAddress` → 请求 Host(去 port)兜底,跟 panel 路径同款。 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "$BASE/inbounds/3/links?host=cdn.example.com"
```

**响应示例:**

```json
{ "data": ["vless://uuid-1@cdn.example.com:443?...#alice", "vless://...#bob"] }
```

| 响应 | 类型 | 说明 |
|---|---|---|
| data | string[] | share URI 列表,每个 client 一条 |

---

### `GET /inbounds/:id/links/by-email`

**v2.5+** 单入站所有 email 客户端的 `{link, qrcode}` 索引,一次拉齐 + 直接渲染二维码用。

`qrcode` 是中等纠错率(M)、256x256 的 PNG QR,以完整 `data:image/png;base64,...` data URL 形式返回 — 前端可直接 `<img src=...>`,curl 也能 `jq -r .data.alice.qrcode | sed 's/.*,//' | base64 -d > alice.png` 一气呵成存盘。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| host | 否 | string | 同 `/inbounds/:id/links`,可省自动兜底 |

**响应示例:**

```json
{
  "data": {
    "alice": {
      "link":   "vless://uuid-1@cdn.example.com:443?...#alice",
      "qrcode": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA..."
    },
    "bob":   { "link": "vless://...#bob", "qrcode": "data:image/png;base64,..." }
  }
}
```

只覆盖**有 email** 的多用户协议(VLESS / VMess / Trojan / SS-2022)。SS-legacy 这种 inbound 级别的链接不在这里返回 —— 用 `/inbounds/:id/links` 拿。

---

### `GET /inbounds/:id/clients/:email/share`

**v2.5+** 单个 email 客户端的 `{link, qrcode}` 直查。`/links/by-email` 的单点版本,业务系统按 email 取一条更直观。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| email | 是 | string | URL 路径段 |
| host | 否 | string | 同上,可省 |

**响应示例:**

```json
{ "data": { "link": "vless://...#alice", "qrcode": "data:image/png;base64,..." } }
```

email 不在该 inbound 的 settings.clients[] 里(或协议不支持 share link)→ `404 client_not_found`。

---

### `GET /inbounds/:id/subscription`

单入站合并的 base64 订阅。响应 `Content-Type: text/plain; charset=utf-8`,**不**包裹 data 外壳。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| host | 是 | string | 节点对外域名 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "$BASE/inbounds/3/subscription?host=cdn.example.com"
```

**响应示例(原始):**

```
dmxlc3M6Ly91dWlkLTFAY2RuLmV4YW1wbGUuY29tOjQ0Mz8uLi4jYWxpY2UKdmxlc3M6Ly...
```

---

### `GET /subscription`

全部启用入站合并,适合一次给客户端 app。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| host | 是 | string | 节点对外域名 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "$BASE/subscription?host=cdn.example.com"
```

响应同 `/inbounds/:id/subscription`,只是合并了所有启用入站的链接。

---

## 屏蔽规则(Block Rule)

命中规则的流量被路由到 `outbound: blocked`(黑洞),效果等同于 drop。多条规则按 `(InboundTag, Type)` 分组合并以减少 xray routing.rules 数量。

### BlockRule 对象

| 字段 | 类型 | 说明 |
|---|---|---|
| id | int | 主键 |
| type | string | `domain` / `ip` / `geosite` / `geoip` / `port` / `protocol` / `source` |
| value | string | 单值或逗号分隔多值;`type` 决定语义(见下) |
| remark | string | 备注 |
| inboundTag | string | 留空 = 全局;指定 tag = 仅对该入站生效 |
| enable | bool | 是否启用 |
| createdAt | int64 | unix 毫秒 |

`value` 按 `type` 解读:
| type | value 例 |
|---|---|
| domain | `example.com,bad.example` |
| ip | `1.2.3.4,5.6.7.0/24` |
| geosite | `geosite:cn`、`geosite:category-ads-all` |
| geoip | `geoip:cn`、`geoip:private` |
| port | `80,443,1000-2000` |
| protocol | `tls,bittorrent,quic` |
| source | 源 IP/CIDR |

---

### `GET /block-rules`

全部规则。

无参数。

**响应示例:**

```json
{
  "data": [
    { "id": 1, "type": "protocol", "value": "bittorrent", "remark": "no BT",
      "inboundTag": "", "enable": true, "createdAt": 1735689600000 }
  ]
}
```

---

### `GET /block-rules/presets`

内置预设(广告、BT、私网等)。

无参数。

**响应示例:**

```json
{
  "data": [
    { "key": "ads",         "name": "屏蔽广告", "rules": [...] },
    { "key": "bittorrent",  "name": "屏蔽 BT",  "rules": [...] },
    { "key": "private-net", "name": "屏蔽私网", "rules": [...] }
  ]
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| key | string | 预设标识,`apply-preset` 用 |
| name | string | 中文名 |
| rules | object[] | 该预设包含的规则模板 |

---

### `POST /block-rules`

创建规则。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| type | 是 | string | 见 type 表 |
| value | 是 | string | 单值或逗号分隔多值 |
| remark | 否 | string | 备注 |
| inboundTag | 否 | string | 留空 = 全局 |
| enable | 否 | bool | 默认 false |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/block-rules -d '{
    "type":"protocol","value":"bittorrent","remark":"no BT","enable":true
  }'
```

**响应示例:** 完整 BlockRule 对象(含 id / createdAt)。

---

### `PUT /block-rules/:id`

整条覆盖。字段同 POST。

**响应示例:** 更新后的 BlockRule 对象。

---

### `PATCH /block-rules/:id/enable`

启停规则。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| enable | 是 | bool | true/false |

**响应示例:**

```json
{ "data": { "id": 1, "enable": false } }
```

---

### `DELETE /block-rules/:id`

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |

**响应示例:**

```json
{ "data": { "id": 1, "deleted": true } }
```

---

### `POST /block-rules/apply-preset`

应用预设(预设里若干条规则会一次性写入)。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| key | 是 | string | 预设 key,见 `/block-rules/presets` |
| inboundTag | 否 | string | 留空 = 全局 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/block-rules/apply-preset -d '{"key":"bittorrent","inboundTag":""}'
```

**响应示例:**

```json
{ "data": { "applied": "bittorrent" } }
```

---

## TLS 证书

存储位置由环境变量 `NEXCORE_CERT_DIR` 决定,默认 `/root/cert`。文件命名 `<name>.cer` + `<name>.key`,权限 0600。

### CertEntry 对象

| 字段 | 类型 | 说明 |
|---|---|---|
| name | string | 证书名(字母 / 数字 / 点 / 横杠;不允许路径分隔符) |
| certPath | string | cer 文件绝对路径 |
| keyPath | string | key 文件绝对路径 |
| notBefore | int64 | unix 秒,证书生效时间 |
| notAfter | int64 | unix 秒,证书过期时间 |
| subject | string | 证书 Subject 字段 |
| dnsNames | string[] | SAN 里的 DNS 名 |
| acmeManaged | bool | 是否由内置 acme.sh 管理(自动续期) |
| panelBound | bool | 是否被面板自身 webCertFile / webKeyFile 引用 |

---

### `GET /certs`

证书列表。

无参数。

**响应示例:**

```json
{
  "data": [
    { "name": "example.com",
      "certPath": "/root/cert/example.com.cer",
      "keyPath":  "/root/cert/example.com.key",
      "notBefore": 1735689600,
      "notAfter":  1767225600,
      "subject":  "CN=example.com",
      "dnsNames": ["example.com","www.example.com"],
      "acmeManaged": true,
      "panelBound":  false }
  ]
}
```

---

### `POST /certs`

上传证书。落盘前用 `tls.X509KeyPair` 校验 PEM 对,不合法返回 `cert_save_failed`。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| name | 是 | string | 字母 / 数字 / 点 / 横杠;不允许路径分隔符 |
| cert | 是 | string | PEM 证书内容 |
| key | 是 | string | PEM 私钥内容 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/certs -d "{
    \"name\":\"example.com\",
    \"cert\":\"$(awk '{printf "%s\\n",$0}' fullchain.pem)\",
    \"key\":\"$(awk '{printf "%s\\n",$0}' privkey.pem)\"
  }"
```

**响应示例:** 完整 CertEntry 对象。

---

### `DELETE /certs/:name`

删除证书(`panelBound=true` 的也会被删,**注意面板自身的 TLS 会失效**)。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| name | 是 | string | URL 路径段 |

**响应:** HTTP 204,无 body。

---

## 访问日志

每个 `/api/v1/*` 调用都会被中间件记一条(包括 `/health`)。bodies **不**记录,只记 meta。

### APILog 对象

| 字段 | 类型 | 说明 |
|---|---|---|
| id | int | 主键 |
| at | int64 | unix 毫秒 |
| method | string | HTTP method |
| path | string | 请求路径 |
| status | int | HTTP 状态码 |
| durationMs | int64 | 处理耗时(毫秒) |
| ip | string | 客户端 IP |
| tokenName | string | 命中的 token 名;`legacy` 表示 legacy token;`""` 表示无 token |

---

### `GET /access-logs`

分页查询。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| path | 否 | string | 路径子串模糊 |
| method | 否 | string | 精确匹配 |
| token | 否 | string | token 名精确匹配 |
| status | 否 | int | HTTP 状态码精确匹配 |
| page | 否 | int | 从 1 开始 |
| size | 否 | int | 每页条数 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "$BASE/access-logs?status=429&size=20&page=1"
```

**响应示例:**

```json
{
  "data": [
    { "id": 123, "at": 1735689600000, "method": "POST",
      "path": "/api/v1/inbounds", "status": 201,
      "durationMs": 17, "ip": "10.0.0.1", "tokenName": "controller-prod" }
  ],
  "meta": { "total": 4321, "page": 1, "size": 50 }
}
```

> 此端点的响应永远带 `meta`,即使没传 `page`。

---

### `DELETE /access-logs`

清空日志表。无参数。

**响应示例:**

```json
{ "data": { "deleted": 1234 } }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| deleted | int | 被清掉的行数 |

---

## 面板设置

### `GET /settings`

返回全部面板设置(含订阅白名单、webhook URL、xray 模板、面板 TLS 配置等所有可配字段)。

无参数。

**响应示例(节选):**

```jsonc
{
  "data": {
    "webPort": 54321,
    "webBasePath": "/",
    "subAllowedHosts": "node.example.com,cdn.example.com",
    "onlineWebhookUrl":    "https://ops.example.com/online",
    "onlineWebhookSecret": "<48 chars>",
    "onlineWebhookNodeId": "node-A",
    "xrayTemplateConfig":  "<JSON 字符串>"
    /* ... */
  }
}
```

---

### `PATCH /settings`

部分更新。**merge 语义**:把请求体反序列化进当前设置对象,**没传的字段保留原值**。

要改子项时,传最小化 body 即可:

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH $BASE/settings -d '{"subAllowedHosts": "node.example.com"}'
```

**响应示例:** 全部设置(更新后)。

---

### `POST /settings/api-token/rotate`

重签 legacy token,**旧的立即失效**。**只影响 legacy 单 token,不影响 multi-token 表里的 token**。

无参数。

**响应示例:**

```json
{ "data": { "token": "<48 chars 明文>" } }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| token | string | 新 legacy token,**只在此处出现一次** |

---

## API Token 管理

### Token 对象

| 字段 | 类型 | 说明 |
|---|---|---|
| id | int | 主键 |
| name | string | 业务名 |
| scope | string | `admin` / `readonly` / `subscription` |
| createdAt | int64 | unix 毫秒 |
| lastUsedAt | int64 | unix 毫秒;**异步更新,可能落后几秒** |
| expiresAt | int64 | unix 毫秒;0 = 永不过期 |
| revoked | bool | 是否已撤销 |

`POST /tokens` **额外**返回 `token` 字段(明文),这是唯一能拿到明文的地方,DB 只存 SHA256。

---

### `GET /tokens`

列表(不含明文)。

无参数。

**响应示例:**

```json
{
  "data": [
    { "id": 7, "name": "controller-prod", "scope": "admin",
      "createdAt": 1735689600000, "lastUsedAt": 1735690000000,
      "expiresAt": 0, "revoked": false }
  ]
}
```

---

### `POST /tokens`

创建 token。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| name | 是 | string | 业务名,DB 不强制唯一但建议唯一 |
| scope | 否 | string | `admin` / `readonly` / `subscription`,默认 `admin` |
| ttlSeconds | 否 | int64 | 0 = 永不过期 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/tokens -d '{
    "name":"alice-sub","scope":"subscription","ttlSeconds":2592000
  }'
```

**响应示例:**

```json
{
  "data": {
    "id": 7,
    "name": "alice-sub",
    "token": "<48 chars 明文>",
    "scope": "subscription",
    "createdAt": 1735689600000,
    "expiresAt": 1738281600000
  }
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| token | string | **只在此处出现一次**,DB 不存明文 |
| 其他 | | 见 [Token 对象](#token-对象) |

---

### `PATCH /tokens/:id`

仅改名。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |
| name | 是 | string | 新名字 |

**响应示例:** 完整 Token 对象(不含明文)。

---

### `POST /tokens/:id/revoke`

标记为已撤销,token 立即失效。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| id | 是 | int | URL 路径段 |

**响应示例:**

```json
{ "data": { "revoked": true } }
```

---

### `DELETE /tokens/:id`

物理删除。

**响应:** HTTP 204,无 body。

---

## Magic-Link 登录

业务系统给运维一条免密、一次性的 web 登录链接 —— 用户点开,前端把 fragment 里的 token POST 给后端换 session,token 立即失效。

### `POST /login-tokens`

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| ttlSeconds | 否 | int | 0 = 用服务端默认(通常 5~10 分钟) |
| note | 否 | string | 纯日志用 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/login-tokens -d '{"ttlSeconds":300,"note":"ops handoff"}'
```

**响应示例:**

```json
{
  "data": {
    "token":       "<明文,只此一次>",
    "createdAt":   1735689600000,
    "expiresAt":   1735690200000,
    "note":        "ops handoff",
    "relativeUrl": "/magic-login#tk=<token>",
    "hint":        "prepend the panel's public origin (scheme + host[:port]) to relativeUrl"
  }
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| token | string | 明文,只此一次 |
| createdAt / expiresAt | int64 | unix 毫秒 |
| note | string | 原样返回 |
| relativeUrl | string | 把 token 放在 URL fragment(`#`),不会进访问日志 / 代理日志 / Referer |
| hint | string | 提示如何拼成完整 URL |

业务系统拿到后只需要拼上 `https://<panel-host>` 前缀分发给运维。

---

## 系统

### `GET /system/listening-ports`

本机所有 LISTEN socket(gopsutil 视角),含其他进程占用的端口。

无参数。

**响应示例:**

```json
{
  "data": [
    { "family": "tcp", "addr": "0.0.0.0", "port": 22, "pid": 1234, "status": "LISTEN" }
  ]
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| family | string | `tcp` / `tcp6` / `udp` / `udp6` |
| addr | string | 监听地址 |
| port | uint32 | 端口 |
| pid | int32 | 占用进程 PID |
| status | string | socket 状态 |

---

### `GET /system/check-port`

端口占用查询(`/system/listening-ports` 的语法糖)。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| port | 是 | int | query 参数,1..65535 |

**请求示例:**

```bash
curl -H "Authorization: Bearer $TOKEN" "$BASE/system/check-port?port=443"
```

**响应示例(已占用):**

```json
{
  "data": {
    "available": false,
    "owner": { "family": "tcp", "addr": "0.0.0.0", "port": 443, "pid": 9999, "status": "LISTEN" }
  }
}
```

**响应示例(空闲):**

```json
{ "data": { "available": true } }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| available | bool | 是否空闲 |
| owner | object | 占用此端口的 ListeningPort,空闲时不出现 |

---

### `GET /system/update-check`

查询 GitHub 上是否有新版本。

无参数。

**响应示例:**

```json
{
  "data": {
    "current":   "2.0.14",
    "latest":    "2.0.15",
    "hasUpdate": true,
    "url":       "https://github.com/.../releases/tag/v2.0.15",
    "publishedAt": 1735689600000
  }
}
```

---

### `POST /system/update-apply`

触发自更新。

| 参数 | 必填 | 类型 | 说明 |
|---|---|---|---|
| version | 否 | string | 指定目标版本(如 `v2.0.15`),不传 = 最新 |

**响应示例:**

```json
{ "data": { "applied": true, "from": "2.0.14", "to": "2.0.15" } }
```

> 升级流程会在新版面板起来后自动重启,期间 HTTP 连接会断。建议升级后等 10s 再轮询 `/health`。

---

### `POST /system/restart-panel`

给面板自身发 `SIGHUP`,main.go 收到后重载 web server。

无参数。

**响应示例:**

```json
{ "data": { "restarting": true } }
```

> HTTP 连接会在重载期间断开,**调用方必须能容忍这条响应可能没拿到**。

---

## 跨节点 Webhook(在线 IP 推送)

面板每 5 秒拍一次"在线 client × 来源 IP"快照,与上次成功推送的状态做 diff,**有变化才发**。失败下个 tick 自动重试(因为 snapshot 是幂等覆盖语义,不需要补发)。

业务用途:多节点配额聚合(每客户最多 N 设备 = N 个 IP,跨多个节点共享)。

### 配置

通过 `PATCH /settings` 配置三项:

| 字段 | 类型 | 说明 |
|---|---|---|
| onlineWebhookUrl | string | 接收方 URL,留空 = 不推送 |
| onlineWebhookSecret | string | HMAC-SHA256 密钥(留空 = 不签名,不建议) |
| onlineWebhookNodeId | string | 节点 ID(留空时退化到 hostname) |

### 推送语义

- **完整覆盖**:接收方应该把 `node_id` 名下的状态全清掉,用 body 里的 snapshot 覆盖。snapshot 里没出现的 email 视为该节点上不在线。
- **diff-only**:与上次成功推送状态相同时不发(降低无意义流量)。
- **debounce**:5 秒一次,高频闪连闪断不会发出 N 个 webhook。
- **失败重试**:5xx / 网络错保留 lastSent 状态,下个 tick 自然重试;4xx 也重试(签名 / 路径错业务方需要修),只在面板日志告警。
- **2xx 即视为成功**,任何 2xx 响应体都不会被解析。

### 推送数据结构

请求:`POST <onlineWebhookUrl>`,`Content-Type: application/json`,`User-Agent: nexcore-x-ui-webhook`。

```json
{
  "node_id": "node-A",
  "ts": 1735689600,
  "online": {
    "alice": ["1.2.3.4"],
    "bob":   ["5.6.7.8", "9.10.11.12"]
  }
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| node_id | string | 节点 ID(取自 `onlineWebhookNodeId`,缺失退化到 hostname) |
| ts | int64 | unix **秒**(注意:与 API 其他地方的 unix 毫秒不同) |
| online | `map[string][]string` | email → IP 列表,IP 已字母排序便于接收方比对 |

### 验签

如果配了 `onlineWebhookSecret`,请求会带头:

```
X-Nx-Signature: <hex(HMAC-SHA256(secret, raw_body))>
```

签名对**原始 body 字节**计算,不重新序列化、不排序。接收方应使用 raw body(中间件未做改写的版本)验签。

> 没配 secret 时不会带 `X-Nx-Signature` 头,接收方必须自己拒绝无签名请求(否则接受任何来源的伪造推送)。

#### 验签示例(curl 接收端用 nginx + 简单脚本伪验)

```bash
# 接收侧伪代码(任何后端语言用同一思路)
SECRET="xxxxx"
SIG_FROM_HEADER="..."
RAW_BODY="$(cat /tmp/raw.json)"
CALC=$(printf '%s' "$RAW_BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $2}')
[ "$CALC" = "$SIG_FROM_HEADER" ] && echo "valid" || echo "invalid"
```

### 接收端推荐实现

```text
1. 读 raw body 字节,**不要**先解析 JSON 再重新 marshal(会改变字节布局,签名失败)
2. 取 X-Nx-Signature,常量时间比较(避免时序攻击)
3. JSON 解析 body,得 node_id / ts / online
4. 用 node_id 作 key,把该节点状态全清掉,用 online 覆盖
5. 返回 HTTP 200,任何 body 都行
```

### 当前限制

- **没有 notify_id**:面板没给每条推送签独立 UUID。snapshot 完整覆盖语义下重复处理是无害的(同一份状态),但接收方无法基于 ID 做"严格一次"幂等。如果业务要求严格幂等,可以用 `(node_id, ts, hash(online))` 作为去重 key。
- **没有重试日志查询**:失败重试只记 panel 日志,面板没暴露持久化推送日志接口。
- **没有"测试推送"端点**:业务系统接入时验证签名只能通过观察自然 tick 来做。

---

## 入站完整画像 / 凭据 / 二维码

业务系统经常需要"对一个入站做完整画像":列出客户端、看每个 client 的流量 / 配额 / 是否在线 / 来自哪些 IP、拿到他们的连接凭据、再渲染成二维码。这条链路涉及多个端点,这里集中说明调用顺序。

### 第一步:列表 → 详情

```bash
# 1) 列表(默认裁剪掉 settings 等大字段)
curl -H "Authorization: Bearer $TOKEN" "$BASE/inbounds"

# 2) 拿到 id=3 后查完整对象(含 settings.clients[])
curl -H "Authorization: Bearer $TOKEN" "$BASE/inbounds/3"
```

`GET /inbounds/:id` 永远返回完整对象。响应里 `settings`、`streamSettings`、`sniffing` 都是 JSON 字符串,业务系统侧需要 `JSON.parse` 一次才能读到 `clients[]`。

### 第二步:客户端清单

```bash
curl -H "Authorization: Bearer $TOKEN" "$BASE/inbounds/3/clients"
```

返回的就是 `inbound.settings.clients[]` 数组,各协议形状不同,见 [Client 对象](#client-对象各协议)。

### 第三步:流量 / 配额 / 启用状态

```bash
curl -H "Authorization: Bearer $TOKEN" "$BASE/inbounds/3/client-traffics"
curl -H "Authorization: Bearer $TOKEN" "$BASE/clients/alice/traffic"
```

### 第四步:在线状态 / 来源 IP

```bash
curl -H "Authorization: Bearer $TOKEN" "$BASE/online-ips-by-email"
# → {"alice": ["1.2.3.4"], "bob": []}
```

判定 alice 在线:`len(byEmail["alice"]) > 0`。空数组 ≠ 永久离线,只是"最近 60s 没在 access.log 出现过流量"。

### 第五步:分享链接(凭据)

> "密钥"在 v2ray 生态里不是单独的字段,而是编码在 share URI 里的整体 ——
> `vless://<UUID>@<host>:<port>?...&flow=xtls-rprx-vision#<remark>` 一行就包含
> 协议、UUID/密码、流控、TLS 配置、SNI、传输方式等所有连接所需信息。

```bash
# 整入站所有 client 的分享链接
curl -H "Authorization: Bearer $TOKEN" "$BASE/inbounds/3/links?host=cdn.example.com"
# → ["vless://...#alice", "vless://...#bob"]

# 整入站的订阅(base64 + text/plain)
curl -H "Authorization: Bearer $TOKEN" "$BASE/inbounds/3/subscription?host=cdn.example.com"

# 全部启用入站合并的订阅
curl -H "Authorization: Bearer $TOKEN" "$BASE/subscription?host=cdn.example.com"
```

终端用户场景应先签发 `subscription` scope token(长期有效),把订阅 URL + token 一起交给客户端 app:

```bash
TOKEN_SUB=$(curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/tokens -d '{"name":"alice-sub","scope":"subscription","ttlSeconds":2592000}' \
  | jq -r '.data.token')
```

### 第六步:二维码

二维码在前端渲染,后端只产 share URI。任何 QR 库都能直接把 URI 编码成图。后端不内置 QR 渲染是有意为之 —— PNG/SVG 比 URI 大几个数量级,大量节点 / client 的批量场景下后端渲染会成瓶颈。

### 一句话决策表

| 场景 | 端点 |
|---|---|
| 列表展示 | `GET /inbounds`(默认裁剪) |
| 编辑某入站 | `GET /inbounds/:id`(完整) |
| 查客户端 | `GET /inbounds/:id/clients` |
| 查流量 / 配额 | `GET /inbounds/:id/client-traffics` 或 `GET /clients/:email/traffic` |
| 改流量上限 / 到期 / 启停 | `PATCH /clients/:email/limits` |
| 查在线 IP | `GET /online-ips-by-email`(per-client)/ `GET /online-ips/:tag`(per-入站) |
| 拿分享链接 | `GET /inbounds/:id/links?host=...` |
| 拿订阅(整入站) | `GET /inbounds/:id/subscription?host=...` |
| 拿订阅(所有入站合并) | `GET /subscription?host=...` |
| 二维码 | 前端用 share URI / 订阅 URL 自行渲染 |

### 一条命令拼某入站 360 视图

```bash
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/inbounds/3" -o /tmp/in.json
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/inbounds/3/client-traffics" -o /tmp/tr.json
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/online-ips-by-email" -o /tmp/ip.json
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/inbounds/3/links?host=cdn.example.com" -o /tmp/lk.json
jq -n --slurpfile in /tmp/in.json --slurpfile tr /tmp/tr.json \
      --slurpfile ip /tmp/ip.json --slurpfile lk /tmp/lk.json \
  '{inbound: $in[0].data, traffics: $tr[0].data, online: $ip[0].data, links: $lk[0].data}'
```

---

## 常见问题(FAQ)

### admin / readonly / subscription 三种 scope 怎么选?

**admin**:业务系统对接(创建/管理入站、客户端、配额、屏蔽规则)。能改任何状态,泄漏代价最高。**readonly**:监控、Dashboard、Prometheus 抓取等只读场景。即使泄漏也只能查不能改。**subscription**:终端用户拉订阅。能调的端点只有三条,泄漏只暴露分享链接(本身就是凭据,所以这种 token 应当短 TTL + 配 `subAllowedHosts` 白名单)。建议每个对接系统单独签发对应 scope 的 token,不要全用 admin 凑合。

### legacy token 和 multi-token 是什么关系?

**legacy** 是面板首次启动自动生成的单 token,存在 `settings.apiToken` 字段里(数据库字段),scope 永远是 admin。**multi-token** 是 `api_tokens` 表里的多条 token,每条独立配 scope / TTL / 业务名,DB 只存 SHA256 哈希。两者鉴权时 multi-token 优先匹配,匹配不上才回落到 legacy。生产环境建议:用 `POST /tokens` 给每个业务系统签独立 token,然后 `POST /settings/api-token/rotate` 把 legacy token 重置成一个不外发的"应急"凭据。

### `/online-ips*` 一直返回空数组怎么办?

数据来自 xray access.log 60s 滑窗,以下场景会拿到空:1) xray 没启 access log(检查 xray 模板的 `log.access` 字段是否配了)2) access.log 文件被轮转 / rename / 截断(panel 会自动重新打开,但**第一秒**可能丢)3) 入站没真实流量(纯握手不写 access)4) 面板刚重启,内存窗口尚未填充(等 60s)。注意空数组只代表"最近 60s 没在 log 里见到",不代表绝对离线。

### xray 改了配置之后什么时候生效?

每个写操作(创建/更新/删除入站、CRUD 客户端、改配额、改 block-rules、改 xray template 等)都会标记 xray 为 `needsRestart`。**不立刻 reload**,而是由 panel 的心跳 job 在下一次 tick 把所有 dirty 操作合并 reload —— 这样连续多次写不会触发 N 次 xray 重启。要立刻生效就调 `POST /xray/restart`。

### 创建入站时 settings 字段为什么是字符串不是嵌套 JSON?

xray-core 的配置每个版本都在加新字段(reality / xhttp / vision / encryption / encryption mode 等),如果面板把它建模成 Go struct,每加一个字段都要发版。把 `settings` / `streamSettings` / `sniffing` 当字符串透传,xray-core 自己做语义解析,**面板不需要随 xray 升级**。代价是对接者要先把对象 `JSON.stringify` 成字符串再放进字段。

### 删除入站后,该入站下 client 的历史流量记录(`client_traffics`)会一起删吗?

**不会**。`client_traffics` 表按 email 全局唯一,与 inbound 解耦,删除入站只清入站自身行,流量记录保留。如果要清 client_traffics 行,删入站后用 SQL 直接删,或者**复用同名 email 创建新入站时它会被自动接管**(因为 email 列是 unique 索引,新入站会复用旧行,up/down 累计也保留)。

### 怎么完全关闭对外的分享 / 订阅功能?

两条路:1) **吊销所有 subscription scope token**,再撤销 admin/readonly token 的 `/inbounds/:id/links`、`/inbounds/:id/subscription`、`/subscription` 三条端点访问 —— 这要写代码,不推荐。2) **配空白名单**:`PATCH /settings` 把 `subAllowedHosts` 设成一个不存在的 host(如 `disabled.invalid`),所有 `?host=...` 都不会过白名单,返回 `400 invalid_host`。第二种是软关闭,推荐。

### Webhook 推送丢失会怎样?需要做幂等吗?

设计上是 **snapshot 完整覆盖语义**:每条推送是节点当前完整在线状态的快照,接收方拿到后应该把 `node_id` 名下的状态全清再用 body 覆盖。所以**丢一条没问题** —— 下个 5s tick(如果状态有变化)又会发一条新的,新的就是当前真值。**重复处理也无害**,因为同一份 snapshot 覆盖结果一致。但面板**没有给每条推送签 notify_id**,如果业务要求严格"恰好一次"语义,接收方应自己用 `(node_id, ts, sha256(online))` 当去重 key。

### Magic-link 链接被截胡有什么风险?

token 在 URL **fragment**(`#tk=...`)而非 path/query,fragment 永远不会上传到任何 HTTP 服务器,不会进访问日志、代理日志、Referer。截胡风险点:1) 链接走 IM/邮件被中间人截到(应该用端到端加密信道传递)2) 用户机器有恶意浏览器扩展读 fragment(低概率,但建议短 TTL,默认 5 分钟)3) 用户不小心把链接发到群里(token 用一次就失效,所以"已被点击过"的链接是安全的;但如果运维还没点,任何拿到链接的人都能登录)。建议:`ttlSeconds: 300` + 走加密信道分发。

### 收到 `429 rate_limited` 怎么办?

每个 token 一个 token-bucket,burst 30、refill 10/s。**超出**就返回 429 + `Retry-After: 1`。处理建议:1) 业务系统单 token 高频场景(几秒一次心跳),burst 30 完全够,不会触限;2) 真要高 QPS,把工作负载拆到多个 token —— 不同 token 不共享 bucket;3) **不要**在收到 429 后立即重试,应等 `Retry-After` 秒(目前固定 1)再发。

### timestamp / 时间字段为什么有的是毫秒有的是秒?

API 大部分时间字段是 **unix 毫秒**(`createdAt`、`expiresAt`、`expiryTime`、APILog.at 等),只有 webhook 推送的 `ts` 是 **unix 秒**(因为接收方常用脚本语言 `time.time()` 默认就是秒)。对接时务必看本文档里的字段表;格式不对会导致排序、对比错乱。
