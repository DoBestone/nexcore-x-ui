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
}
