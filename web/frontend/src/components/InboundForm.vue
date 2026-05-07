<script setup lang="ts">
// 入站添加 / 编辑表单。
// 设计取舍:覆盖 vless / vmess / trojan / shadowsocks / socks / http / dokodemo
// 这些常用协议的核心字段;高级 stream 配置(Reality/gRPC/WS 完整选项)
// 通过 streamSettings 原始 JSON 文本框暴露给会写 xray config 的人。
// 对刚上手的用户:协议 + 端口 + 备注 + (vless/trojan)flow + (ss)method
// 已经够开个 inbound 跑起来。
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { postForm } from '@/api/http'
import type { DBInbound, ClientStub } from '@/api/types'

const props = defineProps<{
  mode: 'add' | 'edit'
  inbound: DBInbound | null
  // 已占用端口列表 — 父组件从当前入站列表里抽出来传过来。add 模式下
  // 用它挑一个空闲端口,避免新建入站默认 10000 跟现有入站撞。
  // 不传 = 退化到 10000(老调用方兼容)。
  usedPorts?: number[]
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

// ClientStub moved to api/types.ts so it's reusable.

const visible = ref(true)
const saving = ref(false)

const protocol = ref('vless')
const port = ref(10000)
const listen = ref('')
const remark = ref('')
const enable = ref(true)
const total = ref(0)
const expiryTime = ref<number>(0)
const _expiryDate = ref<Date | null>(null)

// VLESS / VMess client 通用字段
const clientId = ref('')
const flow = ref('xtls-rprx-vision')
const clientEmail = ref('')

// SS 字段
const ssMethod = ref('2022-blake3-aes-128-gcm')
const ssPassword = ref('')

// Trojan 字段
const trojanPassword = ref('')

// stream / sniffing JSON 原文(高级用户用,留默认即可)
const streamSettings = ref('{"network":"tcp"}')
const sniffing = ref('{"enabled":true,"destOverride":["http","tls"]}')

const isMultiUser = computed(() =>
  ['vless', 'vmess', 'trojan'].includes(protocol.value) ||
  (protocol.value === 'shadowsocks' && ssMethod.value.startsWith('2022-blake3-'))
)

function uuidV4(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return crypto.randomUUID()
  }
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0
    const v = c === 'x' ? r : (r & 0x3) | 0x8
    return v.toString(16)
  })
}

function randomPassword(len = 16): string {
  const chars = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'
  let out = ''
  for (let i = 0; i < len; i++) out += chars[Math.floor(Math.random() * chars.length)]
  return out
}

function regenSecrets() {
  if (protocol.value === 'vless' || protocol.value === 'vmess') {
    clientId.value = uuidV4()
  } else if (protocol.value === 'trojan') {
    trojanPassword.value = randomPassword(16)
  } else if (protocol.value === 'shadowsocks') {
    if (ssMethod.value.startsWith('2022-blake3-')) {
      // 2022-blake3-aes-128-gcm 需要 16 字节 base64
      // 2022-blake3-aes-256-gcm 需要 32 字节;这里给够长留服务端校验
      const bytesNeeded = ssMethod.value.includes('256') ? 32 : 16
      const arr = new Uint8Array(bytesNeeded)
      crypto.getRandomValues(arr)
      ssPassword.value = btoa(String.fromCharCode(...arr))
    } else {
      ssPassword.value = randomPassword(16)
    }
  }
}

// 初始化:add 模式给默认值,edit 模式从 inbound 读
function init() {
  if (props.mode === 'edit' && props.inbound) {
    const inb = props.inbound
    protocol.value = inb.protocol
    port.value = inb.port
    listen.value = inb.listen
    remark.value = inb.remark
    enable.value = inb.enable
    total.value = inb.total
    expiryTime.value = inb.expiryTime
    _expiryDate.value = inb.expiryTime > 0 ? new Date(inb.expiryTime) : null
    streamSettings.value = inb.streamSettings || '{"network":"tcp"}'
    sniffing.value = inb.sniffing || '{}'
    try {
      const s = JSON.parse(inb.settings || '{}')
      if (Array.isArray(s.clients) && s.clients.length > 0) {
        const c = s.clients[0] as ClientStub
        clientId.value = c.id || ''
        flow.value = c.flow || ''
        clientEmail.value = c.email || ''
        trojanPassword.value = c.password || ''
      }
      if (s.method) ssMethod.value = s.method as string
      if (s.password) ssPassword.value = s.password as string
    } catch {
      /* ignore */
    }
  } else {
    regenSecrets()
    clientEmail.value = `user-${Date.now().toString().slice(-6)}`
    port.value = pickFreePort()
  }
}

// 从 10000 起向上扫,找第一个不在 usedPorts 里的端口。10000-10999 全占
// 的话回退到一个 11000-65000 之间的随机端口。65535 是 TCP 上限,但 1024 以下
// 的特权端口用户大概率不会主动选,起步就放 10000 兼顾"好记"+ "不撞典型 dev 服务"。
function pickFreePort(): number {
  const used = new Set(props.usedPorts || [])
  for (let p = 10000; p < 11000; p++) {
    if (!used.has(p)) return p
  }
  // 10000-10999 全占了(几乎不可能):随机扔 11000-65000,撞了让后端报错
  return 11000 + Math.floor(Math.random() * (65000 - 11000))
}

watch(
  () => props.inbound,
  () => init(),
  { immediate: true }
)

watch(protocol, () => {
  // 协议变更时刷一次 secrets
  if (props.mode === 'add') regenSecrets()
})

watch(_expiryDate, (d) => {
  expiryTime.value = d ? d.getTime() : 0
})

function buildSettings(): string {
  switch (protocol.value) {
    case 'vless':
      return JSON.stringify({
        clients: [
          {
            id: clientId.value || uuidV4(),
            flow: flow.value || '',
            email: clientEmail.value
          }
        ],
        decryption: 'none',
        fallbacks: []
      })
    case 'vmess':
      return JSON.stringify({
        clients: [
          {
            id: clientId.value || uuidV4(),
            alterId: 0,
            email: clientEmail.value
          }
        ],
        disableInsecureEncryption: false
      })
    case 'trojan':
      return JSON.stringify({
        clients: [
          {
            password: trojanPassword.value || randomPassword(16),
            email: clientEmail.value
          }
        ],
        fallbacks: []
      })
    case 'shadowsocks':
      // 2022-blake3-* 走 clients[],legacy method 走顶层 password
      if (ssMethod.value.startsWith('2022-blake3-')) {
        return JSON.stringify({
          method: ssMethod.value,
          password: ssPassword.value, // server-side key
          network: 'tcp,udp',
          clients: [{ password: ssPassword.value, email: clientEmail.value }]
        })
      }
      return JSON.stringify({
        method: ssMethod.value,
        password: ssPassword.value,
        network: 'tcp,udp'
      })
    case 'socks':
      return JSON.stringify({
        auth: 'noauth',
        accounts: [],
        udp: true,
        ip: '127.0.0.1'
      })
    case 'http':
      return JSON.stringify({ accounts: [], allowTransparent: false })
    case 'dokodemo-door':
      return JSON.stringify({ address: '', port: 0, network: 'tcp,udp' })
    default:
      return '{}'
  }
}

async function submit() {
  if (!port.value || port.value <= 0) {
    ElMessage.warning('端口必填')
    return
  }
  if (protocol.value === 'shadowsocks' && !ssPassword.value) {
    ElMessage.warning('SS 密码不能为空')
    return
  }
  saving.value = true
  try {
    const data = {
      up: props.mode === 'edit' ? props.inbound?.up || 0 : 0,
      down: props.mode === 'edit' ? props.inbound?.down || 0 : 0,
      total: total.value,
      remark: remark.value,
      enable: enable.value,
      expiryTime: expiryTime.value,
      listen: listen.value,
      port: port.value,
      protocol: protocol.value,
      settings: buildSettings(),
      streamSettings: streamSettings.value,
      sniffing: sniffing.value
    }
    const url =
      props.mode === 'add' ? 'xui/inbound/add' : `xui/inbound/update/${props.inbound!.id}`
    await postForm(url, data)
    ElMessage.success(props.mode === 'add' ? '已创建' : '已更新')
    emit('saved')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="visible"
    :title="mode === 'add' ? '添加入站' : '编辑入站'"
    width="640px"
    class="constrained-dialog"
    :align-center="false"
    @close="emit('close')"
    :close-on-click-modal="false"
  >
    <el-form label-width="120px" label-position="left">
      <el-form-item label="协议">
        <el-select v-model="protocol" :disabled="mode === 'edit'">
          <el-option value="vless" label="VLESS" />
          <el-option value="vmess" label="VMess" />
          <el-option value="trojan" label="Trojan" />
          <el-option value="shadowsocks" label="Shadowsocks" />
          <el-option value="socks" label="Socks" />
          <el-option value="http" label="HTTP" />
          <el-option value="dokodemo-door" label="Dokodemo" />
        </el-select>
        <el-tag v-if="isMultiUser" type="info" size="small" style="margin-left: 8px">
          多用户协议
        </el-tag>
      </el-form-item>

      <el-form-item label="端口">
        <el-input-number v-model="port" :min="1" :max="65535" />
      </el-form-item>

      <el-form-item label="监听 IP">
        <el-input v-model="listen" placeholder="留空 = 所有接口 (0.0.0.0)" />
      </el-form-item>

      <el-form-item label="备注">
        <el-input v-model="remark" />
      </el-form-item>

      <el-form-item label="启用">
        <el-switch v-model="enable" />
      </el-form-item>

      <el-form-item label="总流量上限">
        <el-input-number
          :model-value="total / (1024 * 1024 * 1024)"
          @update:model-value="(v: number) => (total = (v || 0) * 1024 * 1024 * 1024)"
          :min="0"
          :precision="2"
        />
        <span class="nx-muted" style="margin-left: 8px">GB,0 = 不限</span>
      </el-form-item>

      <el-form-item label="到期时间">
        <el-date-picker
          v-model="_expiryDate"
          type="datetime"
          placeholder="留空 = 永不过期"
          format="YYYY-MM-DD HH:mm"
          value-format="x"
        />
      </el-form-item>

      <!-- VLESS / VMess / Trojan / SS-2022 是多用户协议,xray 启动至少需要一个
           clients[] 条目,所以入站表单顺便带出"首个客户(client #1)"的字段。
           创建后 modal 里就直接看到这个客户,后续可在 modal "添加客户端"加更多。 -->
      <el-divider
        v-if="isMultiUser"
        content-position="left"
      >首个客户(client #1) — 入站启动必需</el-divider>
      <template v-if="protocol === 'vless' || protocol === 'vmess'">
        <el-form-item label="UUID">
          <el-input v-model="clientId">
            <template #append>
              <el-button @click="clientId = uuidV4()">重新生成</el-button>
            </template>
          </el-input>
        </el-form-item>
        <el-form-item v-if="protocol === 'vless'" label="flow">
          <el-select v-model="flow">
            <el-option value="" label="无" />
            <el-option value="xtls-rprx-vision" label="xtls-rprx-vision (推荐)" />
          </el-select>
        </el-form-item>
        <el-form-item label="email">
          <el-input v-model="clientEmail" placeholder="作为 stats key 全局唯一" />
          <span class="nx-muted" style="font-size: 12px">
            该客户的标识,创建后会在客户端列表里出现
          </span>
        </el-form-item>
      </template>

      <template v-if="protocol === 'trojan'">
        <el-form-item label="密码">
          <el-input v-model="trojanPassword">
            <template #append>
              <el-button @click="trojanPassword = randomPassword(16)">重新生成</el-button>
            </template>
          </el-input>
        </el-form-item>
        <el-form-item label="email">
          <el-input v-model="clientEmail" />
        </el-form-item>
      </template>

      <template v-if="protocol === 'shadowsocks'">
        <el-form-item label="加密方式">
          <el-select v-model="ssMethod">
            <el-option value="2022-blake3-aes-128-gcm" label="2022-blake3-aes-128-gcm (推荐)" />
            <el-option value="2022-blake3-aes-256-gcm" label="2022-blake3-aes-256-gcm" />
            <el-option value="aes-128-gcm" label="aes-128-gcm (legacy)" />
            <el-option value="aes-256-gcm" label="aes-256-gcm (legacy)" />
            <el-option value="chacha20-poly1305" label="chacha20-poly1305 (legacy)" />
          </el-select>
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="ssPassword">
            <template #append>
              <el-button @click="regenSecrets">重新生成</el-button>
            </template>
          </el-input>
        </el-form-item>
        <el-form-item v-if="ssMethod.startsWith('2022-blake3-')" label="email">
          <el-input v-model="clientEmail" />
        </el-form-item>
      </template>

      <el-divider content-position="left">高级:stream / sniffing</el-divider>

      <el-form-item label="streamSettings">
        <el-input
          v-model="streamSettings"
          type="textarea"
          :rows="3"
          placeholder='{"network":"tcp"}'
        />
      </el-form-item>
      <el-form-item label="sniffing">
        <el-input v-model="sniffing" type="textarea" :rows="2" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="emit('close')">取消</el-button>
      <el-button type="primary" :loading="saving" @click="submit">
        {{ mode === 'add' ? '创建' : '保存' }}
      </el-button>
    </template>
  </el-dialog>
</template>
