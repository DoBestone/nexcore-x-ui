<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { UserFilled, Refresh, ArrowDown } from '@element-plus/icons-vue'
import { http, post, del } from '@/api/http'
import type { ClientTraffic, DBInbound } from '@/api/types'
import { sizeFormat, fmtTimeMs, isExpired, isMultiUserProtocol } from '@/utils/format'
import QrcodeDialog from './QrcodeDialog.vue'

const props = defineProps<{ inbound: DBInbound }>()
const emit = defineEmits<{ (e: 'close', reloaded: boolean): void }>()

const visible = ref(true)
const loading = ref(false)
const rows = ref<ClientTraffic[]>([])
const onlineByEmail = ref<Record<string, string[]>>({})
const linksByEmail = ref<Record<string, string>>({})
// 实时同步的入站 settings:每次 reload() 后回拉一次 inbound,以便孤儿
// 检测准确(props.inbound.settings 是父级 list 取的快照,modal 期间内
// 入站可能被另一处改动)。失败 fallback 用 props.inbound.settings。
const liveSettings = ref<string>(props.inbound.settings || '')
let onlineTimer: number | null = null
let dataChanged = false

// 解析当前 settings.clients[].email 集合,用来识别"孤儿"客户端 —
// 即 client_traffics 表里有行,但 settings.clients[] 已经不再列它。
// 历史 bug:InboundForm 编辑入站把 clients[] 截到 1 条,留下一堆
// modal 看得到却 QR 不出 / 删不掉的孤儿。后端 DeleteClient 现在能
// 清孤儿,前端这里把它们标出来,告诉用户为什么 QR 没了。
const settingsEmails = computed<Set<string>>(() => {
  try {
    const s = JSON.parse(liveSettings.value || '{}') as { clients?: unknown[] }
    if (!Array.isArray(s.clients)) return new Set()
    const out = new Set<string>()
    for (const c of s.clients) {
      const email = (c as { email?: unknown })?.email
      if (typeof email === 'string' && email !== '') out.add(email)
    }
    return out
  } catch {
    return new Set()
  }
})

function isOrphan(email: string): boolean {
  return !settingsEmails.value.has(email)
}

async function reload() {
  loading.value = true
  try {
    const r = await http.get<{ success: boolean; obj: ClientTraffic[] }>(
      `xui/api/inbounds/${props.inbound.id}/client-traffics`
    )
    rows.value = r.data?.obj || []
  } finally {
    loading.value = false
  }
  await Promise.all([refreshOnline(), refreshLinks(), refreshLiveSettings()])
}

async function refreshLiveSettings() {
  try {
    const r = await http.get<{
      success: boolean
      obj?: { settings?: string }
    }>(`xui/api/inbounds/${props.inbound.id}/info`)
    if (typeof r.data?.obj?.settings === 'string') {
      liveSettings.value = r.data.obj.settings
    }
  } catch {
    /* ignore — fallback 已经是 props.inbound.settings */
  }
}

async function refreshOnline() {
  try {
    const r = await http.get<{ success: boolean; obj: Record<string, string[]> }>(
      'xui/api/online-ips-by-email'
    )
    onlineByEmail.value = r.data?.obj || {}
  } catch {
    /* ignore */
  }
}

async function refreshLinks() {
  try {
    const r = await http.get<{ success: boolean; obj: Record<string, string> }>(
      `xui/api/inbounds/${props.inbound.id}/links`
    )
    linksByEmail.value = r.data?.obj || {}
  } catch {
    /* ignore */
  }
}

function onlineCount(email: string): number {
  return onlineByEmail.value[email]?.length || 0
}

function onlineList(email: string): string[] {
  return onlineByEmail.value[email] || []
}

// ---------- 添加 ----------
const addVisible = ref(false)
const newEmail = ref('')
const newId = ref('')
const newFlow = ref('xtls-rprx-vision')
const newPassword = ref('')
const newTotalGB = ref(0)
const newExpiry = ref<Date | null>(null)
const newEnable = ref(true)
const adding = ref(false)

function uuidV4(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
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

// SS-2022(method 以 2022-blake3- 开头)的 user PSK 必须是 base64 编码、
// 长度跟 method 匹配:aes-128-gcm 16 字节 → 24 字符 base64;aes-256-gcm
// 32 字节 → 44 字符 base64。早前 newPassword 走 randomPassword(16) 给的是
// 16 位 ASCII,xray reload 报 "proxy/shadowsocks_2022: bad key"。
function ss2022Method(): string | null {
  if (props.inbound.protocol !== 'shadowsocks') return null
  try {
    const s = JSON.parse(props.inbound.settings || '{}') as { method?: string }
    if (typeof s.method === 'string' && s.method.startsWith('2022-blake3-')) {
      return s.method
    }
  } catch {
    /* ignore */
  }
  return null
}

function genClientPassword(): string {
  const m = ss2022Method()
  if (m) {
    const bytesNeeded = m.includes('256') ? 32 : 16
    const arr = new Uint8Array(bytesNeeded)
    crypto.getRandomValues(arr)
    return btoa(String.fromCharCode(...arr))
  }
  return randomPassword(16)
}

function openAdd() {
  newEmail.value = `user-${Date.now().toString().slice(-6)}`
  newId.value = uuidV4()
  newFlow.value = 'xtls-rprx-vision'
  newPassword.value = genClientPassword()
  newTotalGB.value = 0
  newExpiry.value = null
  newEnable.value = true
  addVisible.value = true
}

async function doAdd() {
  if (!newEmail.value) {
    ElMessage.warning('email 必填')
    return
  }
  const proto = props.inbound.protocol
  const client: Record<string, unknown> = { email: newEmail.value }
  if (proto === 'vless') {
    client.id = newId.value
    if (newFlow.value) client.flow = newFlow.value
  } else if (proto === 'vmess') {
    client.id = newId.value
    client.alterId = 0
  } else if (proto === 'trojan') {
    client.password = newPassword.value
  } else if (proto === 'shadowsocks') {
    client.password = newPassword.value
  } else {
    ElMessage.warning(`协议 ${proto} 不支持客户端管理`)
    return
  }
  adding.value = true
  try {
    await post(`xui/api/inbounds/${props.inbound.id}/clients`, client)
    // 第二步:把额度/到期/启用一次写入 client_traffics。AddClient 那边
    // 用 inbound 级 total/expiry 兜底建行,这里只在用户显式设过非默认
    // 值时才打 PATCH(避免无谓 SQL)。
    const wantTotal = newTotalGB.value > 0
    const wantExpiry = newExpiry.value !== null
    const wantDisable = !newEnable.value
    if (wantTotal || wantExpiry || wantDisable) {
      await post(`xui/api/clients/${encodeURIComponent(newEmail.value)}/limits`, {
        total: wantTotal ? Math.round(newTotalGB.value * 1024 * 1024 * 1024) : 0,
        expiryTime: newExpiry.value ? newExpiry.value.getTime() : 0,
        enable: newEnable.value
      })
    }
    ElMessage.success('已添加客户端')
    addVisible.value = false
    dataChanged = true
    await reload()
  } catch (e: unknown) {
    const msg = (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '添加失败'
    ElMessage.error(msg)
  } finally {
    adding.value = false
  }
}

// ---------- 编辑额度 ----------
const editVisible = ref(false)
const editEmail = ref('')
const editTotalGB = ref(0)
const editExpiry = ref<Date | null>(null)
const editEnable = ref(true)
const saving = ref(false)

function openEdit(r: ClientTraffic) {
  editEmail.value = r.email
  editTotalGB.value = r.total ? Math.round((r.total / (1024 * 1024 * 1024)) * 100) / 100 : 0
  editExpiry.value = r.expiryTime > 0 ? new Date(r.expiryTime) : null
  editEnable.value = r.enable
  editVisible.value = true
}

async function applyEdit() {
  saving.value = true
  try {
    await post(`xui/api/clients/${encodeURIComponent(editEmail.value)}/limits`, {
      total: editTotalGB.value > 0 ? Math.round(editTotalGB.value * 1024 * 1024 * 1024) : 0,
      expiryTime: editExpiry.value ? editExpiry.value.getTime() : 0,
      enable: editEnable.value
    })
    ElMessage.success('已保存')
    editVisible.value = false
    dataChanged = true
    await reload()
  } finally {
    saving.value = false
  }
}

// ---------- 重置流量 ----------
async function resetTraffic(r: ClientTraffic) {
  await post(`xui/api/clients/${encodeURIComponent(r.email)}/reset-traffic`)
  ElMessage.success('已重置')
  dataChanged = true
  await reload()
}

// ---------- 启停 ----------
async function toggleEnable(r: ClientTraffic, v: boolean) {
  try {
    await post(`xui/api/clients/${encodeURIComponent(r.email)}/limits`, { enable: v })
    r.enable = v
    dataChanged = true
  } catch {
    r.enable = !v
  }
}

// ---------- 删除 ----------
async function delClient(r: ClientTraffic) {
  try {
    await ElMessageBox.confirm(`确认删除客户端 ${r.email}?xray reload 后立即失效。`, '删除客户端', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  try {
    await del(`xui/api/inbounds/${props.inbound.id}/clients/${encodeURIComponent(r.email)}`)
    ElMessage.success('已删除')
    dataChanged = true
    await reload()
  } catch (e: unknown) {
    const msg = (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '删除失败'
    ElMessage.error(msg)
  }
}

// ---------- 二维码 ----------
const qrVisible = ref(false)
const qrTitle = ref('')
const qrLink = ref('')

function showQrcode(r: ClientTraffic) {
  const link = linksByEmail.value[r.email]
  if (!link) {
    ElMessage.info('该客户端没有可生成的链接')
    return
  }
  qrTitle.value = r.email
  qrLink.value = link
  qrVisible.value = true
}

// ---------- 当前协议是否多用户 ----------
const supportsClient = isMultiUserProtocol(props.inbound.protocol, props.inbound.settings)

onMounted(async () => {
  if (!supportsClient) {
    ElMessage.info('该协议不支持客户端管理')
    emit('close', false)
    return
  }
  await reload()
  onlineTimer = window.setInterval(() => {
    if (!document.hidden) refreshOnline()
  }, 5000)
})

onBeforeUnmount(() => {
  if (onlineTimer) {
    clearInterval(onlineTimer)
    onlineTimer = null
  }
})
</script>

<template>
  <el-dialog
    :model-value="visible"
    :title="`客户端流量 — ${inbound.remark || 'inbound#' + inbound.id}`"
    width="900px"
    class="constrained-dialog"
    :align-center="true"
    @close="emit('close', dataChanged)"
    :close-on-click-modal="false"
  >
    <div class="head-actions">
      <el-button type="primary" :icon="UserFilled" @click="openAdd">添加客户端</el-button>
      <el-button :icon="Refresh" @click="reload" :loading="loading">刷新</el-button>
      <span class="nx-muted" style="margin-left: 8px">
        VLESS / VMess / Trojan / SS-2022 才有 email 客户
      </span>
    </div>

    <!-- 孤儿提示:有任何一行 client_traffics 不在 settings.clients[] 里,
         给一条非 closable 的 alert,解释发生了什么 + 建议怎么处理。 -->
    <el-alert
      v-if="rows.some((r) => isOrphan(r.email))"
      type="warning"
      :closable="false"
      show-icon
      title="检测到孤儿客户端"
      style="margin-bottom: 12px"
    >
      <template #default>
        下方标记 <el-tag type="warning" size="small">孤儿</el-tag>
        的行已经不在 xray 配置里(连不上、不算流量、二维码也无法生成),
        但流量记录还留着。可以直接「删除」清掉,或在「添加客户端」里用同样的
        email 重建以恢复服务。
      </template>
    </el-alert>

    <el-empty v-if="rows.length === 0" description="该入站还没有 email 客户端" />
    <el-table v-else :data="rows" stripe v-loading="loading">
      <el-table-column prop="email" label="email" min-width="160">
        <template #default="{ row }">
          <span>{{ row.email }}</span>
          <el-tag
            v-if="isOrphan(row.email)"
            type="warning"
            size="small"
            style="margin-left: 6px"
          >孤儿</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="已用 / 上限" min-width="180">
        <template #default="{ row }">
          <span class="nx-mono">{{ sizeFormat(row.up + row.down) }}</span>
          <template v-if="row.total > 0">
            <span class="nx-muted"> / </span>
            <span :class="{ over: row.up + row.down >= row.total }">
              {{ sizeFormat(row.total) }}
            </span>
          </template>
          <el-tag v-else type="success" size="small" style="margin-left: 4px">无限</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="到期" width="160">
        <template #default="{ row }">
          <el-tag v-if="row.expiryTime > 0" :type="isExpired(row.expiryTime) ? 'danger' : 'info'">
            {{ fmtTimeMs(row.expiryTime) }}
          </el-tag>
          <el-tag v-else type="success">永久</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="启用" width="80">
        <template #default="{ row }">
          <el-switch
            :model-value="row.enable"
            :disabled="isOrphan(row.email)"
            @change="(v: boolean) => toggleEnable(row, v)"
          />
        </template>
      </el-table-column>
      <el-table-column label="在线 IP" width="110">
        <template #default="{ row }">
          <el-tooltip
            v-if="onlineCount(row.email) > 0"
            effect="light"
            :content="onlineList(row.email).join('\n')"
          >
            <el-tag type="success">{{ onlineCount(row.email) }} 在线</el-tag>
          </el-tooltip>
          <el-tag v-else type="info">离线</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="240" align="right">
        <template #default="{ row }">
          <!-- 孤儿行禁用「二维码」「编辑」(都依赖 settings.clients[] 里的
               配置:UUID/密码 / 限额对一个不存在的 client 没意义);删除走
               孤儿清理路径,后端会安全地只删 client_traffics 行。 -->
          <el-tooltip
            v-if="isOrphan(row.email)"
            effect="light"
            content="该客户已不在 xray 配置中,无法生成二维码。可直接删除孤儿行。"
          >
            <el-button size="small" disabled>二维码</el-button>
          </el-tooltip>
          <el-button v-else size="small" @click="showQrcode(row)">二维码</el-button>
          <el-button
            size="small"
            :disabled="isOrphan(row.email)"
            @click="openEdit(row)"
          >编辑</el-button>
          <el-dropdown trigger="click" @command="(cmd: string) => {
            if (cmd === 'reset') resetTraffic(row)
            else if (cmd === 'del') delClient(row)
          }">
            <el-button size="small">更多 <el-icon><ArrowDown /></el-icon></el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item
                  command="reset"
                  :disabled="isOrphan(row.email)"
                >重置流量</el-dropdown-item>
                <el-dropdown-item command="del" divided>
                  <span style="color: var(--nx-danger)">删除</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </template>
      </el-table-column>
    </el-table>

    <!-- 添加客户端子 dialog -->
    <el-dialog v-model="addVisible" title="添加客户端" width="480px" append-to-body class="constrained-dialog" :align-center="true">
      <el-form label-width="100px" label-position="left">
        <el-form-item label="email">
          <el-input v-model="newEmail" />
        </el-form-item>
        <template v-if="inbound.protocol === 'vless' || inbound.protocol === 'vmess'">
          <el-form-item label="UUID">
            <el-input v-model="newId">
              <template #append>
                <el-button @click="newId = uuidV4()">生成</el-button>
              </template>
            </el-input>
          </el-form-item>
          <el-form-item v-if="inbound.protocol === 'vless'" label="flow">
            <el-select v-model="newFlow">
              <el-option value="" label="无" />
              <el-option value="xtls-rprx-vision" label="xtls-rprx-vision" />
            </el-select>
          </el-form-item>
        </template>
        <template v-if="inbound.protocol === 'trojan' || inbound.protocol === 'shadowsocks'">
          <el-form-item label="密码">
            <el-input v-model="newPassword">
              <template #append>
                <el-button @click="newPassword = genClientPassword()">生成</el-button>
              </template>
            </el-input>
            <span v-if="ss2022Method()" class="nx-muted" style="font-size: 12px">
              SS-2022 user PSK 须为 base64,长度与 method 匹配(自动生成已处理)
            </span>
          </el-form-item>
        </template>
        <el-divider content-position="left">额度(可选)</el-divider>
        <el-form-item label="流量上限">
          <el-input-number v-model="newTotalGB" :min="0" :precision="2" />
          <span class="nx-muted" style="margin-left: 8px">GB,0 = 不限</span>
        </el-form-item>
        <el-form-item label="到期时间">
          <el-date-picker v-model="newExpiry" type="datetime" placeholder="留空 = 永不过期" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="newEnable" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="addVisible = false">取消</el-button>
        <el-button type="primary" :loading="adding" @click="doAdd">添加</el-button>
      </template>
    </el-dialog>

    <!-- 编辑额度 -->
    <el-dialog v-model="editVisible" title="编辑额度" width="480px" append-to-body class="constrained-dialog" :align-center="true">
      <el-form label-width="100px" label-position="left">
        <el-form-item label="email">
          <el-input v-model="editEmail" disabled />
        </el-form-item>
        <el-form-item label="流量上限">
          <el-input-number v-model="editTotalGB" :min="0" :precision="2" />
          <span class="nx-muted" style="margin-left: 8px">GB,0 = 不限</span>
        </el-form-item>
        <el-form-item label="到期时间">
          <el-date-picker v-model="editExpiry" type="datetime" placeholder="留空 = 永不过期" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="editEnable" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="applyEdit">保存</el-button>
      </template>
    </el-dialog>

    <QrcodeDialog v-model="qrVisible" :title="qrTitle" :link="qrLink" />
  </el-dialog>
</template>

<style scoped>
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}
.over {
  color: var(--nx-danger);
  font-weight: 600;
}
</style>
