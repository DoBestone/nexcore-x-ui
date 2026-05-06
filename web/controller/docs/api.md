# NexCore x-ui — REST API v1

Base path: `<panel base>/api/v1` (default `/api/v1`).
All non-health endpoints require a bearer token.

## Authentication

The token is generated on first server start and printed to the log **once**:

```
INFO ... API token generated (record now, will not be shown again): <48-char>
```

Lost it? `POST /api/v1/settings/api-token/rotate` mints a new one.

| Where | How |
|---|---|
| Preferred | `Authorization: Bearer <token>` |
| Alt | `X-API-Token: <token>` |
| Last resort | `?api_token=<token>` |

## Response envelope

```jsonc
// success
{"data": <object | array | null>}

// failure
{"error": true, "code": "...", "message": "...", "details": ...}
```

---

## Endpoint Map

```
GET    /health                                   no auth
GET    /server/status

GET    /xray/status
POST   /xray/restart
GET    /xray/config                              effective JSON sent to xray
GET    /xray/logs                                last ~100 lines of xray stdout/stderr
GET    /xray/template                            xray base config template (string)
PUT    /xray/template                            body = raw JSON template

# Inbounds
GET    /inbounds                                 ?protocol= &enable= &tag= &search= &page= &size=
GET    /inbounds/:id
POST   /inbounds                                 single create
PUT    /inbounds/:id                             full update
PATCH  /inbounds/:id/enable                      body {"enable": bool}
DELETE /inbounds/:id
POST   /inbounds/:id/reset-traffic
POST   /inbounds/bulk                            body = []Inbound
PATCH  /inbounds/bulk-enable                     body {"ids":[..],"enable":bool}
POST   /inbounds/bulk-delete                     body {"ids":[..]}
POST   /inbounds/reset-all-traffic               body {"ids":[..]}, omit for all

# Clients (within an inbound's settings.clients[])
GET    /inbounds/:id/clients
POST   /inbounds/:id/clients                     body = client object
PUT    /inbounds/:id/clients/:identifier         body = client object
DELETE /inbounds/:id/clients/:identifier

# Share / subscription
GET    /inbounds/:id/links?host=...              JSON array of share URIs
GET    /inbounds/:id/subscription?host=...       text/plain base64
GET    /subscription?host=...                    text/plain base64 (all enabled inbounds)

# Traffic
GET    /traffic                                  DB cumulative
GET    /traffic/live                             xray gRPC realtime (resets counters!)

# Settings
GET    /settings
PATCH  /settings
POST   /settings/api-token/rotate

# Tokens (multi-token)
GET    /tokens
POST   /tokens                                   body {"name": "..."} → token shown ONCE
PATCH  /tokens/:id                               body {"name": "..."}
POST   /tokens/:id/revoke
DELETE /tokens/:id

# Certs (under NEXCORE_CERT_DIR or /root/cert)
GET    /certs
POST   /certs                                    body {"name","cert","key"} (PEM)
DELETE /certs/:name

# System
GET    /system/listening-ports                   gopsutil view of every TCP-LISTEN/UDP socket
GET    /system/check-port?port=...               {available: bool, owner?}
POST   /system/restart-panel                     soft-restart via SIGHUP
```

---

## Resource details

### Inbound listing filters

| Param | Type | Description |
|---|---|---|
| `protocol` | string | exact match (`vless`, `vmess`, ...) |
| `enable` | `true`/`false` | filter by enable flag |
| `tag` | string | exact match |
| `search` | string | substring of remark or tag |
| `page` | int (1-based) | enable pagination |
| `size` | int | page size; capped at 200; default 50 |

When `page` is set, response shape is `{data: [...], meta: {total, page, size}}`.

### Inbound shape (POST/PUT body)

```jsonc
{
  "remark":         "string",
  "enable":         true,
  "expiryTime":     0,                  // unix ms; 0 = no expiry
  "total":          0,                  // bytes; 0 = unlimited
  "listen":         "",                 // empty = 0.0.0.0
  "port":           10086,
  "protocol":       "vless",            // vmess|vless|trojan|shadowsocks|socks|http|Dokodemo-door|wireguard
  "settings":       "<raw xray JSON>",
  "streamSettings": "<raw xray JSON>",
  "tag":            "inbound-10086",
  "sniffing":       "<raw xray JSON>"
}
```

`settings` / `streamSettings` / `sniffing` are JSON **strings** passed through
to xray. Reality, XHTTP, Vision, etc. work as long as the embedded JSON is
valid for the running xray-core.

### Client objects (per protocol)

```jsonc
// VLESS:    {"id": "<UUID>", "flow": "xtls-rprx-vision", "email": "alice"}
// VMess:    {"id": "<UUID>", "alterId": 0,               "email": "alice"}
// Trojan:   {"password": "...",                          "email": "alice"}
// SS-2022:  {"password": "...",                          "email": "alice"}
```

`identifier` in the URL is `email` for all protocols.

### Subscription / link generation

`?host=` is **required** — the panel can't reliably know the public address
of the node, so the caller supplies it (could be an IP, domain, or a CDN
fronting domain). The subscription endpoints are plain `text/plain` and
emit the canonical base64 blob that V2RayN, Shadowrocket, etc. expect.

### Tokens

Plaintext is returned **only** at create time:

```jsonc
// POST /tokens {"name":"system-A"}
{"data": {"id":1,"name":"system-A","token":"<48 chars>","createdAt":...}}
```

`lastUsedAt` updates async on every successful auth.

### Certs

`POST /certs` validates the PEM pair via `tls.X509KeyPair` before persisting;
invalid pairs return `cert_save_failed`. Files are stored as
`<dir>/<name>.cer` and `<dir>/<name>.key` with mode 0600. Override the
location with env `NEXCORE_CERT_DIR` (default `/root/cert`).

### System

`POST /system/restart-panel` sends `SIGHUP` to its own pid; main.go reloads
the web server. The HTTP connection drops mid-flight, so calls must tolerate
that.

---

## Error codes

| Group | Codes |
|---|---|
| Auth | `missing_api_token` `invalid_api_token` `api_token_not_configured` `auth_db_error` |
| Path / body | `invalid_id` `invalid_body` `invalid_json` `invalid_port` `host_required` |
| Inbounds | `inbound_not_found` `create_failed` `update_failed` `delete_failed` `reset_failed` |
| Clients | `client_identifier_required` `client_not_found` `client_duplicate` `unsupported_protocol` `client_op_failed` |
| Tokens | `token_not_found` `revoke_failed` `rename_failed` |
| Certs | `cert_save_failed` `cert_delete_failed` `cert_list_failed` |
| Subscription | `subscription_failed` |
| xray | `xray_restart_failed` `xray_traffic_failed` `xray_config_failed` |
| System | `system_query_failed` |
| Backend | `db_error` `internal_error` `rotate_failed` |

---

## Examples

```bash
TOKEN=...
BASE=http://node:54321/api/v1

# 1. Find a free port, then create a VLESS+Reality inbound
curl -H "Authorization: Bearer $TOKEN" "$BASE/system/check-port?port=443"

curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds -d '{
    "remark":"node-A","enable":true,"port":443,"protocol":"vless","tag":"inbound-443",
    "settings":"{\"clients\":[{\"id\":\"<UUID>\",\"flow\":\"xtls-rprx-vision\",\"email\":\"alice\"}],\"decryption\":\"none\"}",
    "streamSettings":"{\"network\":\"tcp\",\"security\":\"reality\",\"realitySettings\":{...}}",
    "sniffing":"{\"enabled\":true,\"destOverride\":[\"http\",\"tls\"]}"
  }'

# 2. Add a second client to that inbound
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/inbounds/1/clients -d '{
    "id":"<UUID2>","flow":"xtls-rprx-vision","email":"bob"
  }'

# 3. Subscribe — give it to the customer's app
curl "$BASE/subscription?host=cdn.example.com" \
  -H "Authorization: Bearer $TOKEN"

# 4. Provision a per-system API token for the controller
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/tokens -d '{"name":"controller-prod"}'

# 5. Bulk disable a set of inbounds during maintenance
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH $BASE/inbounds/bulk-enable -d '{"ids":[1,2,3],"enable":false}'

# 6. Inspect what xray is actually running
curl -H "Authorization: Bearer $TOKEN" $BASE/xray/config
curl -H "Authorization: Bearer $TOKEN" $BASE/xray/logs

# 7. Upload a TLS cert
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST $BASE/certs -d "{
    \"name\":\"example.com\",
    \"cert\":\"$(awk '{printf "%s\\n",$0}' fullchain.pem)\",
    \"key\":\"$(awk '{printf "%s\\n",$0}' privkey.pem)\"
  }"
```
