<script setup lang="ts">
// 出站服务器配置页 — 管理"中转出口"。每条出站登记一个 vless/vmess/trojan/ss
// 远端目标,xray 启动时把它注入到 outbounds[];入站表单可以选"绑这条出站",
// 后端 routing 规则把入站流量导出到这条出站(所谓前置入站 + 后置出站架构)。
//
// settings / streamSettings 是 raw JSON,语义跟 xray 文档一致 — 这一页面
// 主要面向懂 xray 的人,我们给 vless/vmess/trojan/ss 各提供一个最小可用模板,
// 用户用模板按钮 prefill 后改 address/port/uuid 即可。复杂场景(reality / WS /
// gRPC)用户自己进 streamSettings 文本框补字段。
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Delete } from '@element-plus/icons-vue'
import { postForm } from '@/api/http'
import type { Outbound } from '@/api/types'

const rows = ref<Outbound[]>([])
const loading = ref(false)

async function reload() {
  loading.value = true
  try {
    rows.value = (await postForm<Outbound[]>('xui/outbound/list')) || []
  } finally {
    loading.value = false
  }
}

// ---------- form ----------
const formVisible = ref(false)
const formMode = ref<'add' | 'edit'>('add')
const editing = ref<Partial<Outbound>>({})

const PROTOCOLS = ['vless', 'vmess', 'trojan', 'shadowsocks'] as const

// 各协议给一个最小可用的 settings 模板 — vnext 一行 + 一个 user。SS 是
// servers + method + password 直球。streamSettings 默认 tcp,用户按需改。
const settingsTemplate: Record<(typeof PROTOCOLS)[number], (addr: string, port: number) => string> = {
  vless: (addr, port) =>
    JSON.stringify(
      {
        vnext: [
          {
            address: addr || 'remote.example.com',
            port: port || 443,
            users: [
              {
                id: 'replace-with-uuid',
                encryption: 'none',
                flow: ''
              }
            ]
          }
        ]
      },
      null,
      2
    ),
  vmess: (addr, port) =>
    JSON.stringify(
      {
        vnext: [
          {
            address: addr || 'remote.example.com',
            port: port || 443,
            users: [{ id: 'replace-with-uuid', alterId: 0 }]
          }
        ]
      },
      null,
      2
    ),
  trojan: (addr, port) =>
    JSON.stringify(
      {
        servers: [
          {
            address: addr || 'remote.example.com',
            port: port || 443,
            password: 'replace-with-password'
          }
        ]
      },
      null,
      2
    ),
  shadowsocks: (addr, port) =>
    JSON.stringify(
      {
        servers: [
          {
            address: addr || 'remote.example.com',
            port: port || 443,
            method: '2022-blake3-aes-128-gcm',
            password: 'replace-with-base64-psk'
          }
        ]
      },
      null,
      2
    )
}

function openAdd() {
  formMode.value = 'add'
  editing.value = {
    tag: 'relay-' + Date.now().toString(36).slice(-5),
    name: '',
    protocol: 'vless',
    address: '',
    port: 443,
    settings: settingsTemplate.vless('', 443),
    streamSettings: JSON.stringify({ network: 'tcp' }, null, 2),
    remark: '',
    enable: true
  }
  importLink.value = ''
  formVisible.value = true
}

// ---------- 分享链接导入 ----------
// 用户从客户端 / 上游面板复制一条 vmess:// vless:// trojan:// ss:// 进来,
// 一键解析填表,免得手撸 settings JSON。所有解析在浏览器侧 — 链接里有
// uuid/密码,塞给后端再去解纯属让密码白经过一段网络。
const importLink = ref('')

interface ParsedLink {
  protocol: 'vless' | 'vmess' | 'trojan' | 'shadowsocks'
  address: string
  port: number
  name: string // ps / fragment,作为 outbound 名称建议
  settings: object
  streamSettings: object
}

// base64 兼容解码:vmess 链接通常是标准 base64,vless/ss 的 fragment 内
// userinfo 可能是 url-safe (-_) 没 padding。两种都试。
function tryB64(s: string): string {
  const trimmed = s.trim().replace(/\s+/g, '')
  for (const candidate of [trimmed, trimmed.replace(/-/g, '+').replace(/_/g, '/')]) {
    const padded = candidate + '==='.slice((candidate.length + 3) % 4)
    try {
      const bin = atob(padded)
      // utf8 解码 — 中文 ps 字段在 vmess JSON 里常见
      return decodeURIComponent(
        bin
          .split('')
          .map((c) => '%' + c.charCodeAt(0).toString(16).padStart(2, '0'))
          .join('')
      )
    } catch {
      /* try next */
    }
  }
  return ''
}

// vmess net 字段 → xray streamSettings.network。tls 字段 + sni/host 决定 security。
function vmessStream(j: Record<string, unknown>): Record<string, unknown> {
  const net = (j.net as string) || 'tcp'
  const stream: Record<string, unknown> = { network: net }
  switch (net) {
    case 'ws':
      stream.wsSettings = {
        path: (j.path as string) || '/',
        headers: (j.host as string) ? { Host: j.host as string } : undefined
      }
      break
    case 'grpc':
      stream.grpcSettings = { serviceName: (j.path as string) || '' }
      break
    case 'h2':
    case 'http':
      stream.network = 'h2'
      stream.httpSettings = {
        host: (j.host as string) ? [(j.host as string)] : [],
        path: (j.path as string) || '/'
      }
      break
  }
  const tls = (j.tls as string) || ''
  if (tls === 'tls') {
    stream.security = 'tls'
    const sni = (j.sni as string) || (j.host as string) || ''
    if (sni) stream.tlsSettings = { serverName: sni }
  } else if (tls === 'reality') {
    // reality 需要 pbk / sid / sni / fp 等;链接里能带就带,缺的字段
    // 用户去 streamSettings 文本框补全。
    stream.security = 'reality'
    stream.realitySettings = {
      serverName: (j.sni as string) || '',
      publicKey: (j.pbk as string) || '',
      shortId: (j.sid as string) || '',
      fingerprint: (j.fp as string) || ''
    }
  }
  return stream
}

// vless / trojan 的 query string 解析为 streamSettings。逻辑与 vmessStream
// 同构(network/security 来源不同字段而已),复用一段 helper。
function queryStream(q: URLSearchParams): Record<string, unknown> {
  const net = q.get('type') || 'tcp'
  const stream: Record<string, unknown> = { network: net }
  switch (net) {
    case 'ws':
      stream.wsSettings = {
        path: q.get('path') || '/',
        headers: q.get('host') ? { Host: q.get('host')! } : undefined
      }
      break
    case 'grpc':
      stream.grpcSettings = { serviceName: q.get('serviceName') || '' }
      break
    case 'h2':
    case 'http':
      stream.network = 'h2'
      stream.httpSettings = {
        host: q.get('host') ? [q.get('host')!] : [],
        path: q.get('path') || '/'
      }
      break
  }
  const security = q.get('security') || ''
  if (security === 'tls') {
    stream.security = 'tls'
    const sni = q.get('sni') || q.get('host') || ''
    if (sni) stream.tlsSettings = { serverName: sni }
  } else if (security === 'reality') {
    stream.security = 'reality'
    stream.realitySettings = {
      serverName: q.get('sni') || '',
      publicKey: q.get('pbk') || '',
      shortId: q.get('sid') || '',
      fingerprint: q.get('fp') || ''
    }
  }
  return stream
}

function parseShareLink(raw: string): ParsedLink {
  const text = raw.trim()
  if (!text) throw new Error('链接不能为空')

  // ---- vmess ----
  if (text.startsWith('vmess://')) {
    const decoded = tryB64(text.slice('vmess://'.length))
    if (!decoded) throw new Error('vmess 链接 base64 解码失败')
    let j: Record<string, unknown>
    try {
      j = JSON.parse(decoded)
    } catch {
      throw new Error('vmess 链接解码后不是合法 JSON')
    }
    const address = String(j.add || '')
    const port = Number(j.port) || 0
    if (!address || !port) throw new Error('vmess 链接缺少 add/port')
    const id = String(j.id || '')
    if (!id) throw new Error('vmess 链接缺少 id')
    return {
      protocol: 'vmess',
      address,
      port,
      name: String(j.ps || ''),
      settings: {
        vnext: [
          {
            address,
            port,
            users: [{ id, alterId: Number(j.aid) || 0, security: 'auto' }]
          }
        ]
      },
      streamSettings: vmessStream(j)
    }
  }

  // ---- vless / trojan ----  都是 RFC 3986 URI 形态
  if (text.startsWith('vless://') || text.startsWith('trojan://')) {
    const isVless = text.startsWith('vless://')
    let u: URL
    try {
      u = new URL(text)
    } catch {
      throw new Error('链接格式不合法')
    }
    const address = u.hostname
    const port = Number(u.port) || (u.protocol === 'trojan:' ? 443 : 0)
    if (!address || !port) throw new Error('链接缺少 host/port')
    const userinfo = decodeURIComponent(u.username)
    if (!userinfo) throw new Error(isVless ? '链接缺少 uuid' : '链接缺少 password')
    const name = decodeURIComponent(u.hash.slice(1))
    const stream = queryStream(u.searchParams)
    if (isVless) {
      return {
        protocol: 'vless',
        address,
        port,
        name,
        settings: {
          vnext: [
            {
              address,
              port,
              users: [
                {
                  id: userinfo,
                  encryption: 'none',
                  flow: u.searchParams.get('flow') || ''
                }
              ]
            }
          ]
        },
        streamSettings: stream
      }
    }
    return {
      protocol: 'trojan',
      address,
      port,
      name,
      settings: {
        servers: [{ address, port, password: userinfo }]
      },
      streamSettings: stream
    }
  }

  // ---- ss (SIP002 + SS-2022 multi-user) ----
  // 两种形态:
  //   ss://base64(method:password)@host:port#name      ← legacy / SS-2022 单用户
  //   ss://base64(method:password@host:port)#name      ← 老旧整体编码
  //   ss://base64(method:server_psk:user_psk)@host:port#name ← SS-2022 多用户
  if (text.startsWith('ss://')) {
    const body = text.slice('ss://'.length)
    const hashIdx = body.indexOf('#')
    const beforeHash = hashIdx >= 0 ? body.slice(0, hashIdx) : body
    const name = hashIdx >= 0 ? decodeURIComponent(body.slice(hashIdx + 1)) : ''
    const at = beforeHash.indexOf('@')
    let method = ''
    let password = ''
    let address = ''
    let port = 0
    if (at >= 0) {
      const userinfo = tryB64(beforeHash.slice(0, at)) || decodeURIComponent(beforeHash.slice(0, at))
      const hostport = beforeHash.slice(at + 1)
      const portColon = hostport.lastIndexOf(':')
      address = hostport.slice(0, portColon)
      port = Number(hostport.slice(portColon + 1))
      const parts = userinfo.split(':')
      if (parts.length === 2) {
        method = parts[0]
        password = parts[1]
      } else if (parts.length >= 3) {
        // SS-2022 multi-user:method:server_psk:user_psk → xray client
        // password 字段填 server_psk:user_psk(冒号拼)
        method = parts[0]
        password = parts.slice(1).join(':')
      }
    } else {
      // 老形态:整段 base64 包含 method:password@host:port
      const decoded = tryB64(beforeHash)
      const m = decoded.match(/^([^:]+):(.+)@([^:]+):(\d+)$/)
      if (!m) throw new Error('ss 链接格式无法识别')
      method = m[1]
      password = m[2]
      address = m[3]
      port = Number(m[4])
    }
    if (!address || !port || !method || !password) {
      throw new Error('ss 链接字段不完整')
    }
    return {
      protocol: 'shadowsocks',
      address,
      port,
      name,
      settings: { servers: [{ address, port, method, password }] },
      streamSettings: { network: 'tcp' }
    }
  }

  throw new Error('未识别的链接前缀(支持 vmess:// vless:// trojan:// ss://)')
}

function importFromLink() {
  try {
    const r = parseShareLink(importLink.value)
    editing.value.protocol = r.protocol
    editing.value.address = r.address
    editing.value.port = r.port
    editing.value.settings = JSON.stringify(r.settings, null, 2)
    editing.value.streamSettings = JSON.stringify(r.streamSettings, null, 2)
    // 名称只在用户没填时建议(以免覆盖手动输入);备注里也把原 ps 抄一份
    if (!editing.value.name && r.name) editing.value.name = r.name
    if (r.name && !editing.value.remark) editing.value.remark = `from: ${r.name}`
    ElMessage.success('已导入,请检查并补全 UUID / 密码 / TLS 相关字段')
  } catch (e: unknown) {
    const msg = (e as Error).message || '解析失败'
    ElMessage.error(msg)
  }
}

function openEdit(r: Outbound) {
  formMode.value = 'edit'
  editing.value = { ...r }
  formVisible.value = true
}

// 用户切协议时如果 settings 还是上一个协议的 template(逐字符匹配),自动
// 替换为新协议的 template;改过的 settings 不动 — 避免"改了几行 UUID 又
// 切协议把内容洗掉"。
const cachedTemplates = computed<Record<string, string>>(() => {
  const addr = editing.value.address || ''
  const port = editing.value.port || 0
  return {
    vless: settingsTemplate.vless(addr, port),
    vmess: settingsTemplate.vmess(addr, port),
    trojan: settingsTemplate.trojan(addr, port),
    shadowsocks: settingsTemplate.shadowsocks(addr, port)
  }
})

function onProtocolChange(newProto: string) {
  const cur = (editing.value.settings || '').trim()
  const oldTemplates = Object.values(cachedTemplates.value).map((s) => s.trim())
  if (!cur || oldTemplates.includes(cur)) {
    editing.value.settings = cachedTemplates.value[newProto]
  }
  editing.value.protocol = newProto
}

function applyTemplate() {
  const proto = editing.value.protocol || 'vless'
  editing.value.settings = settingsTemplate[proto as keyof typeof settingsTemplate](
    editing.value.address || '',
    editing.value.port || 0
  )
}

async function save() {
  const e = editing.value
  if (!e.tag || !e.name || !e.protocol || !e.address) {
    ElMessage.warning('tag / 名称 / 协议 / 地址 必填')
    return
  }
  if (!e.port || e.port <= 0 || e.port > 65535) {
    ElMessage.warning('端口越界')
    return
  }
  // settings / streamSettings 至少是合法 JSON,后端再校验一次,这里给即时反馈
  for (const f of ['settings', 'streamSettings'] as const) {
    if (!(e[f] || '').trim()) continue
    try {
      JSON.parse(e[f] as string)
    } catch {
      ElMessage.warning(`${f} 不是合法 JSON`)
      return
    }
  }
  const data = {
    tag: e.tag,
    name: e.name,
    protocol: e.protocol,
    address: e.address,
    port: e.port,
    settings: e.settings || '',
    streamSettings: e.streamSettings || '',
    remark: e.remark || '',
    enable: e.enable ?? true
  }
  try {
    if (formMode.value === 'edit' && e.id) {
      await postForm(`xui/outbound/update/${e.id}`, data)
    } else {
      await postForm('xui/outbound/add', data)
    }
    ElMessage.success('已保存')
    formVisible.value = false
    await reload()
  } catch (err: unknown) {
    const msg = (err as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '保存失败'
    ElMessage.error(msg)
  }
}

async function del(r: Outbound) {
  try {
    await ElMessageBox.confirm(
      `确认删除出站 ${r.name}(tag=${r.tag})?\n所有引用此出站的入站会被自动解绑回直连。`,
      '删除出站',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  await postForm(`xui/outbound/del/${r.id}`)
  ElMessage.success('已删除')
  await reload()
}

async function toggle(r: Outbound, v: boolean) {
  try {
    await postForm(`xui/outbound/toggle/${r.id}`, { enable: v })
    r.enable = v
  } catch {
    r.enable = !v
  }
}

// ---------- 连通测试 ----------
// 后端 TCP dial + 可选 TLS 握手,不发起完整代理握手 — 这是面板里"链路通不通"
// 的最小信号,不是"代理凭据对不对"。结果用 message 字段 + reachable / tlsOk
// 拼一条 toast:
//   - 不通          → ElMessage.error  + 完整错误
//   - TCP 通 / TLS 失败 → ElMessage.warning(链路通但 TLS 配置有问题)
//   - 都通          → ElMessage.success
interface OutboundTestResult {
  reachable: boolean
  latencyMs: number
  tlsOk: boolean
  tlsError?: string
  message: string
}

const testingId = ref<number | null>(null)

async function testConnectivity(r: Outbound) {
  testingId.value = r.id
  try {
    const result = await postForm<OutboundTestResult>(`xui/outbound/test/${r.id}`)
    if (!result.reachable) {
      ElMessage.error(result.message)
      return
    }
    if (result.tlsError) {
      ElMessage.warning(result.message)
      return
    }
    ElMessage.success(result.message)
  } catch (e: unknown) {
    const msg = (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '测试失败'
    ElMessage.error(msg)
  } finally {
    testingId.value = null
  }
}

onMounted(reload)
</script>

<template>
  <div class="nx-page">
    <h2>出站配置</h2>

    <div class="nx-card overview">
      <div class="hint">
        <p class="nx-muted" style="margin: 0">
          配置中转出口节点 — vless/vmess/trojan/ss。绑到入站后,该入站的流量会
          经此出站转发,并把分享链接的名称前缀替换为本出站的「名称」。删除时
          会自动解绑所有引用入站。
        </p>
      </div>
      <div class="actions">
        <el-button type="primary" :icon="Plus" @click="openAdd">添加出站</el-button>
        <el-button :icon="Refresh" @click="reload" :loading="loading">刷新</el-button>
      </div>
    </div>

    <el-card>
      <el-empty v-if="rows.length === 0" description="还没有配置出站" />
      <el-table v-else :data="rows" stripe v-loading="loading">
        <el-table-column label="启用" width="70">
          <template #default="{ row }">
            <el-switch :model-value="row.enable" @change="(v: boolean) => toggle(row, v)" />
          </template>
        </el-table-column>
        <el-table-column prop="tag" label="tag" width="160" />
        <el-table-column prop="name" label="名称" min-width="120" />
        <el-table-column prop="protocol" label="协议" width="110">
          <template #default="{ row }">
            <el-tag>{{ row.protocol }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="地址" min-width="220">
          <template #default="{ row }">
            <span class="nx-mono">{{ row.address }}:{{ row.port }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="备注" min-width="120" />
        <el-table-column label="操作" width="240" align="right">
          <template #default="{ row }">
            <el-button
              size="small"
              type="success"
              plain
              :loading="testingId === row.id"
              @click="testConnectivity(row)"
            >测试</el-button>
            <el-button size="small" @click="openEdit(row)">编辑</el-button>
            <el-button size="small" type="danger" plain :icon="Delete" @click="del(row)" />
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- add / edit -->
    <el-dialog
      v-model="formVisible"
      :title="formMode === 'add' ? '添加出站' : '编辑出站'"
      width="640px"
      class="constrained-dialog"
      :align-center="true"
      :close-on-click-modal="false"
    >
      <!-- 分享链接快捷导入(仅 add 模式)— 把 vmess:// / vless:// / trojan:// /
           ss:// 一键解析成下面的字段。需要 reality / 多 user 这种高级配置
           还得手动改 settings JSON,但能省去 90% 的 boilerplate。 -->
      <div v-if="formMode === 'add'" class="import-box">
        <div class="import-label">
          从分享链接导入(vmess / vless / trojan / ss)
        </div>
        <el-input
          v-model="importLink"
          type="textarea"
          :rows="2"
          placeholder="vmess://eyJhZGQiOiI0Ny44Mi41LjE3OS...   或者 vless://uuid@host:port?...   或者 ss://...."
          class="nx-mono-input"
        />
        <div style="margin-top: 6px">
          <el-button size="small" type="primary" :disabled="!importLink.trim()" @click="importFromLink">
            解析并填入下面字段
          </el-button>
          <span class="nx-muted" style="font-size: 12px; margin-left: 8px">
            导入后请确认 tag / 名称 是否合适;reality 等高级 stream 字段需自己核对
          </span>
        </div>
      </div>

      <el-form label-width="120px" label-position="left">
        <el-form-item label="tag">
          <el-input
            v-model="editing.tag"
            :disabled="formMode === 'edit'"
            placeholder="字母数字 . _ -,routing 用此 tag 引用"
          />
          <span class="nx-muted" style="font-size: 12px">
            创建后不可修改 — 已被入站引用的 tag 一改全失联
          </span>
        </el-form-item>
        <el-form-item label="名称">
          <el-input
            v-model="editing.name"
            placeholder="如:美国中转-2(显示在分享链接 ps 字段)"
          />
        </el-form-item>
        <el-form-item label="协议">
          <el-select
            :model-value="editing.protocol"
            @update:model-value="(v: string) => onProtocolChange(v)"
          >
            <el-option v-for="p in PROTOCOLS" :key="p" :value="p" :label="p" />
          </el-select>
        </el-form-item>
        <el-form-item label="地址">
          <el-input v-model="editing.address" placeholder="远端域名或 IP" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="editing.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="editing.remark" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="editing.enable" />
        </el-form-item>

        <el-divider content-position="left">高级:协议 settings(JSON)</el-divider>
        <el-form-item>
          <el-button size="small" @click="applyTemplate">使用当前协议模板</el-button>
          <span class="nx-muted" style="font-size: 12px; margin-left: 8px">
            点了会用 address/port 重写 settings 模板;请把 UUID/密码改成实际值
          </span>
        </el-form-item>
        <el-form-item label="settings">
          <el-input
            v-model="editing.settings"
            type="textarea"
            :rows="8"
            class="nx-mono-input"
          />
        </el-form-item>
        <el-form-item label="streamSettings">
          <el-input
            v-model="editing.streamSettings"
            type="textarea"
            :rows="4"
            class="nx-mono-input"
            placeholder='{"network":"tcp"}'
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="formVisible = false">取消</el-button>
        <el-button type="primary" @click="save">
          {{ formMode === 'add' ? '创建' : '保存' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.overview {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  margin-bottom: 16px;
  gap: 16px;
}
.hint {
  flex: 1;
  min-width: 0;
}
.actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}
:deep(.nx-mono-input .el-textarea__inner) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.45;
  white-space: pre;
  overflow-x: auto;
}
.import-box {
  background: var(--nx-bg);
  border: 1px dashed var(--nx-border);
  padding: 10px 12px;
  border-radius: 8px;
  margin-bottom: 16px;
}
.import-label {
  font-size: 12px;
  color: var(--nx-text-muted);
  margin-bottom: 6px;
}
</style>
