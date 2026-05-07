// 后端 Status 结构体的 TS 镜像。字段一一对应 web/service/server.go
export interface ServerStatus {
  cpu: number
  mem: { current: number; total: number }
  swap: { current: number; total: number }
  disk: { current: number; total: number }
  xray: { state: 'running' | 'stop' | 'error' | string; errorMsg: string; version: string }
  uptime: number
  loads: number[]
  tcpCount: number
  udpCount: number
  netIO: { up: number; down: number }
  netTraffic: { sent: number; recv: number }
}

export interface DBInbound {
  id: number
  userId: number
  up: number
  down: number
  total: number
  remark: string
  enable: boolean
  expiryTime: number
  listen: string
  port: number
  protocol: string
  settings: string
  streamSettings: string
  tag: string
  sniffing: string
  clientStats?: ClientTraffic[]
}

export interface ClientTraffic {
  id: number
  inboundId: number
  email: string
  up: number
  down: number
  total: number
  expiryTime: number
  enable: boolean
}

export interface BlockRule {
  id: number
  type: string
  value: string
  remark: string
  inboundTag: string
  enable: boolean
  createdAt: number
}

export interface AllSetting {
  webListen: string
  webPort: number
  webCertFile: string
  webKeyFile: string
  webBasePath: string
  tgBotEnable: boolean
  tgBotToken: string
  tgBotChatId: number
  tgRunTime: string
  xrayTemplateConfig: string
  timeLocation: string
  // 在线 IP webhook 推送(给业务系统跨节点聚合用)
  onlineWebhookUrl: string
  onlineWebhookSecret: string
  onlineWebhookNodeId: string
}

// API token row (web/controller/api_panel.go listTokens)
export interface ApiToken {
  id: number
  name: string
  scope: string
  createdAt: number
  expiresAt: number
  lastUsedAt: number
}

// Token plaintext is returned exactly once on create (api_panel.go createToken)
export interface CreatedApiToken extends ApiToken {
  plaintext: string
}

// Magic link create response (controller/setting.go createMagicLink)
export interface MagicLinkResp {
  url: string
  token: string
  expiresAt: number
  ttlSeconds: number
}

// Block-rule preset entry (BlockRule controller listPresets)
export interface BlockRulePreset {
  key: string
  label?: string
  description?: string
  type?: string
  count?: number
}

// Inbound share-link map: email → vless/vmess/trojan/ss URL
// (api_panel.go inboundLinks). Keys are client emails.
export type InboundLinks = Record<string, string>

// /me response (api_panel.go me)
export interface MeInfo {
  id: number
  username: string
  loginUsername?: string
}

// Lightweight client stub used by InboundForm when editing client list
// inside an inbound's settings JSON. Field set is the union of all
// protocol-specific client shapes — populate only what the protocol
// requires.
export interface ClientStub {
  id?: string
  password?: string
  email?: string
  flow?: string
  alterId?: number
}
