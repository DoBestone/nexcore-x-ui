<script setup lang="ts">
// 单用户入站详情查看 — Socks / HTTP / Dokodemo / SS-legacy。
// 多用户协议(VLESS/VMess/Trojan/SS-2022)的连接信息走 ClientTrafficModal
// 行内二维码,这里不重复做。
//
// SS-legacy 链接客户端构造:后端 panel 路由 /xui/api/inbounds/:id/links 走的是
// LinksByEmail,SS-legacy 没 email 出来是空 map,所以历史上"二维码"按钮在
// SS-legacy 上点了报"没有可生成的链接"。这里直接前端拼 ss:// 链接,顺带把
// QR + 基础信息一起给出。
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import type { DBInbound } from '@/api/types'

const props = defineProps<{
  modelValue: boolean
  inbound: DBInbound | null
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

interface SSAccount { user: string; pass: string }

interface Settings {
  // SS
  method?: string
  password?: string
  // Socks / HTTP
  auth?: string
  accounts?: SSAccount[]
  udp?: boolean
  // Dokodemo
  address?: string
  port?: number
  network?: string
}

const parsed = computed<Settings>(() => {
  if (!props.inbound) return {}
  try {
    return JSON.parse(props.inbound.settings || '{}') as Settings
  } catch {
    return {}
  }
})

// 服务器主机:面板访问的 hostname 是绝大多数情况的正解(用户从这里登录就是
// 用这个地址连过来的)。如果 listen 显式绑定到具体 IP,优先用那个;0.0.0.0
// / 空 / 127.0.0.1 这种"占位"地址不要回填到客户端配置。
const host = computed(() => {
  const listen = (props.inbound?.listen || '').trim()
  if (listen && listen !== '0.0.0.0' && listen !== '::' && listen !== '127.0.0.1') {
    return listen
  }
  return window.location.hostname || 'your.server.example'
})

const port = computed(() => props.inbound?.port ?? 0)

// 浏览器侧 ss:// 拼装(legacy method,即非 2022-blake3-)。
// 格式:ss://base64(method:password)@host:port#remark
const ssLegacyLink = computed<string>(() => {
  const inb = props.inbound
  if (!inb || inb.protocol !== 'shadowsocks') return ''
  const m = parsed.value.method
  const pwd = parsed.value.password
  if (!m || m.startsWith('2022-blake3-') || !pwd) return ''
  const userinfo = btoa(unescape(encodeURIComponent(`${m}:${pwd}`)))
  const tag = encodeURIComponent(inb.remark || `inbound#${inb.id}`)
  return `ss://${userinfo}@${host.value}:${port.value}#${tag}`
})

// QR code:仅 SS-legacy 有可分享链接,其它单用户协议没有标准 URI 方案,
// 所以仅此一处加载 qrcode。和 QrcodeDialog 一样走 dynamic import,首屏不背包。
const qrDataUrl = ref('')
watch(
  () => [props.modelValue, ssLegacyLink.value] as const,
  async ([open, link]) => {
    if (open && link) {
      const QRCode = (await import('qrcode')).default
      qrDataUrl.value = await QRCode.toDataURL(link, { width: 220, margin: 1 })
    } else {
      qrDataUrl.value = ''
    }
  },
  { immediate: true }
)

async function copy(text: string) {
  if (!text) return
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(text)
      ElMessage.success('已复制')
      return
    }
  } catch {
    /* fallthrough */
  }
  // http 上下文 fallback,跟 QrcodeDialog 同款 textarea + execCommand
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    ElMessage[ok ? 'success' : 'warning'](ok ? '已复制' : '复制失败,请手动选中复制')
  } catch {
    ElMessage.warning('复制失败,请手动选中复制')
  }
}

// "auth: password" 才有意义的 accounts 列表;noauth 时就算 settings 里塞了
// accounts 也跑不到鉴权路径,展示出来反而误导用户。
const socksAccounts = computed<SSAccount[]>(() => {
  const s = parsed.value
  if (s.auth !== 'password') return []
  return Array.isArray(s.accounts) ? s.accounts : []
})

const httpAccounts = computed<SSAccount[]>(() => {
  const s = parsed.value
  return Array.isArray(s.accounts) ? s.accounts : []
})
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    @update:model-value="(v: boolean) => emit('update:modelValue', v)"
    :title="`连接信息 — ${inbound?.remark || 'inbound#' + inbound?.id}`"
    width="520px"
    class="constrained-dialog"
    :align-center="true"
    :close-on-click-modal="true"
  >
    <div v-if="inbound" class="details">
      <!-- 基础字段:host / port,所有单用户协议公用。Dokodemo 例外 —
           它的"端口"是面板的 listen 端口,目标在 settings 里另列。 -->
      <div class="row">
        <span class="label">协议</span>
        <el-tag>{{ inbound.protocol }}</el-tag>
      </div>
      <div class="row">
        <span class="label">服务器</span>
        <code class="value">{{ host }}</code>
        <el-button size="small" link type="primary" @click="copy(host)">复制</el-button>
      </div>
      <div class="row">
        <span class="label">{{ inbound.protocol === 'dokodemo-door' ? '监听端口' : '端口' }}</span>
        <code class="value">{{ port }}</code>
        <el-button size="small" link type="primary" @click="copy(String(port))">复制</el-button>
      </div>

      <!-- Shadowsocks legacy:method + password + ss:// + QR -->
      <template v-if="inbound.protocol === 'shadowsocks'">
        <div class="row">
          <span class="label">加密方式</span>
          <code class="value">{{ parsed.method || '-' }}</code>
        </div>
        <div class="row">
          <span class="label">密码</span>
          <code class="value mono break">{{ parsed.password || '-' }}</code>
          <el-button size="small" link type="primary" @click="copy(parsed.password || '')">复制</el-button>
        </div>
        <template v-if="ssLegacyLink">
          <el-divider content-position="left">分享链接</el-divider>
          <div class="qr-box">
            <img v-if="qrDataUrl" :src="qrDataUrl" alt="qrcode" />
            <div class="link-text">{{ ssLegacyLink }}</div>
            <el-button size="small" type="primary" @click="copy(ssLegacyLink)">复制链接</el-button>
          </div>
        </template>
        <el-alert
          v-else-if="parsed.method && parsed.method.startsWith('2022-blake3-')"
          type="info"
          :closable="false"
          show-icon
          title="SS-2022 多用户协议"
          description="请在客户端流量里查看每个 email 客户的二维码"
        />
      </template>

      <!-- Socks:auth / accounts / udp -->
      <template v-else-if="inbound.protocol === 'socks'">
        <div class="row">
          <span class="label">鉴权</span>
          <el-tag :type="parsed.auth === 'password' ? 'warning' : 'success'">
            {{ parsed.auth === 'password' ? '账号密码' : '免认证 (noauth)' }}
          </el-tag>
        </div>
        <div class="row">
          <span class="label">UDP</span>
          <el-tag :type="parsed.udp ? 'success' : 'info'">
            {{ parsed.udp ? '启用' : '禁用' }}
          </el-tag>
        </div>
        <template v-if="socksAccounts.length > 0">
          <el-divider content-position="left">账号</el-divider>
          <div v-for="(a, i) in socksAccounts" :key="i" class="row">
            <code class="value mono break">{{ a.user }}:{{ a.pass }}</code>
            <el-button size="small" link type="primary" @click="copy(`${a.user}:${a.pass}`)">复制</el-button>
          </div>
        </template>
      </template>

      <!-- HTTP:accounts -->
      <template v-else-if="inbound.protocol === 'http'">
        <div class="row">
          <span class="label">鉴权</span>
          <el-tag :type="httpAccounts.length > 0 ? 'warning' : 'success'">
            {{ httpAccounts.length > 0 ? '账号密码' : '免认证(匿名)' }}
          </el-tag>
        </div>
        <template v-if="httpAccounts.length > 0">
          <el-divider content-position="left">账号</el-divider>
          <div v-for="(a, i) in httpAccounts" :key="i" class="row">
            <code class="value mono break">{{ a.user }}:{{ a.pass }}</code>
            <el-button size="small" link type="primary" @click="copy(`${a.user}:${a.pass}`)">复制</el-button>
          </div>
        </template>
      </template>

      <!-- Dokodemo:转发目标在 settings.address / settings.port -->
      <template v-else-if="inbound.protocol === 'dokodemo-door'">
        <div class="row">
          <span class="label">目标地址</span>
          <code class="value">{{ parsed.address || '-' }}</code>
          <el-button
            v-if="parsed.address"
            size="small"
            link
            type="primary"
            @click="copy(parsed.address || '')"
          >复制</el-button>
        </div>
        <div class="row">
          <span class="label">目标端口</span>
          <code class="value">{{ parsed.port ?? '-' }}</code>
        </div>
        <div class="row">
          <span class="label">网络</span>
          <code class="value">{{ parsed.network || 'tcp' }}</code>
        </div>
      </template>
    </div>
  </el-dialog>
</template>

<style scoped>
.details {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.label {
  width: 80px;
  flex-shrink: 0;
  color: var(--nx-text-muted);
  font-size: 13px;
}
.value {
  background: var(--nx-bg);
  padding: 3px 8px;
  border-radius: 4px;
  font-size: 13px;
}
.value.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
.value.break {
  word-break: break-all;
  flex: 1;
  min-width: 0;
}
.qr-box {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
}
.qr-box img {
  width: 220px;
  height: 220px;
  background: #fff;
  border-radius: 6px;
}
.link-text {
  font-family: ui-monospace, monospace;
  font-size: 12px;
  color: var(--nx-text-muted);
  word-break: break-all;
  text-align: center;
  background: var(--nx-bg);
  padding: 8px;
  border-radius: 6px;
  width: 100%;
  max-height: 80px;
  overflow-y: auto;
}
</style>
