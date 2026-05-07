// 字节数 → 人类可读;沿用旧前端 sizeFormat 同样的语义
export function sizeFormat(bytes: number): string {
  if (!bytes || bytes < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  let n = bytes
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(i === 0 ? 0 : 2)} ${units[i]}`
}

// 秒数 → 1d 2h 3m 形式。Server status uptime 单位是秒。
export function uptimeFormat(seconds: number): string {
  if (!seconds || seconds < 0) return '0s'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  if (d > 0) return `${d}d ${h}h ${m}m`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

// 毫秒时间戳 → YYYY-MM-DD HH:mm
export function fmtTimeMs(ms: number): string {
  if (!ms || ms <= 0) return '-'
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function isExpired(ms: number): boolean {
  return ms > 0 && ms <= Date.now()
}

// 协议判定 — 是否多用户(对齐后端 isMultiUser 语义)
export function isMultiUserProtocol(protocol: string, settings: string): boolean {
  if (protocol === 'vmess' || protocol === 'vless' || protocol === 'trojan') return true
  if (protocol === 'shadowsocks') {
    try {
      const s = JSON.parse(settings || '{}') as { method?: string }
      return typeof s.method === 'string' && s.method.startsWith('2022-blake3-')
    } catch {
      return false
    }
  }
  return false
}

// 提取 inbound.settings.clients[] 数量
export function clientCount(settings: string): number {
  try {
    const s = JSON.parse(settings || '{}') as { clients?: unknown[] }
    return Array.isArray(s.clients) ? s.clients.length : 0
  } catch {
    return 0
  }
}
