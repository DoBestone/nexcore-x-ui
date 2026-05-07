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
  // 关联到 Outbound.tag。空字符串 = 直连(走模板的 freedom);非空 = 把
  // 该入站的流量定向到指定出站做中转。share link 的 ps 字段用对应
  // Outbound.name 替代节点名作前缀,见后端 ShareService.remarkPrefix。
  outboundTag?: string
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
  // 节点名称(e.g. "香港节点1") — 注入到 share link 的 ps 字段方便
  // 客户端识别。空 = 不加前缀。
  nodeName: string

  // 安全入口:启用后面板需要带 secureEntryPath 才能访问。
  // 启用前必须先填 path,否则保存会被后端拒掉(避免改完自己进不来)。
  secureEntryEnabled: boolean
  secureEntryPath: string

  // 节点地址:分享链接里写的 host。空 = 跟着浏览器访问面板的域名走。
  // CF 橙云代理时必须显式配,否则链接打到 CF 代理上的非标端口超时。
  nodeAddress: string

  // CF API token(Zone:DNS:Edit)。前端 password input,后端加密存。
  // GetAllSetting 返空字符串(不回读明文),空提交 = 不修改(由 setting
  // 服务的 skipIfEmptyKeys 兜)。仅 password 控件用。
  cfApiToken: string
}

// 出站服务器 — 用户配置的中转节点。Inbound 通过 outboundTag 字段关联。
export interface Outbound {
  id: number
  tag: string
  name: string
  protocol: string
  address: string
  port: number
  settings: string
  streamSettings: string
  remark: string
  enable: boolean
  createdAt: number
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
