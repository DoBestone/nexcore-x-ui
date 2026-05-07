<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Delete, User, Connection, ArrowDown } from '@element-plus/icons-vue'
import { http, postForm } from '@/api/http'
import type { DBInbound, Outbound } from '@/api/types'
import { sizeFormat, fmtTimeMs, isExpired, isMultiUserProtocol, clientCount } from '@/utils/format'
import InboundForm from '@/components/InboundForm.vue'
import ClientTrafficModal from '@/components/ClientTrafficModal.vue'
import InboundDetailsDialog from '@/components/InboundDetailsDialog.vue'

const inbounds = ref<DBInbound[]>([])
const loading = ref(false)
const onlineIps = ref<Record<string, string[]>>({})
let onlineTimer: number | null = null

// 出站名称查表 — 入站列表 "出站" 列展示用。后端 Inbound.outboundTag 是 tag,
// 不是名字;为了 UI 上"美国中转-2"这种可读标签,reload 时拉一次出站列表
// 建一个 tag→name 的 map。出站删除会自动解绑入站(后端逻辑),所以这里
// 不会出现 tag 找不到对应 outbound 的边界情况。
const outboundsByTag = ref<Record<string, Outbound>>({})

async function loadOutbounds() {
  try {
    const list = (await postForm<Outbound[]>('xui/outbound/list')) || []
    const m: Record<string, Outbound> = {}
    for (const o of list) m[o.tag] = o
    outboundsByTag.value = m
  } catch {
    outboundsByTag.value = {}
  }
}

function outboundLabel(tag: string): string {
  const ob = outboundsByTag.value[tag]
  return ob ? ob.name : tag
}

// 防火墙状态(/xui/api/firewall-status):后端检测到 UFW / firewalld
// 启用时,把已放行的 TCP 端口列表 + 端口段带回来。前端跟 inbound.port
// 做差集,提示"端口被防火墙拦了,客户端连不进来" —— 历史上反复有用户
// 拿了链接连不上,排了半天发现是 UFW INPUT DROP 没放行。
//
// 端口段(UFW `10000:11000/tcp` / firewalld `10000-11000/tcp`)是 v2.0.16
// 之后才识别的:用户用 `ufw allow 10000:11000/tcp` 一次性放行一段,如果
// 前端只看单端口列表会把段内 inbound 全报"被拦住",误报多到失去信号。
type PortRange = { lo: number; hi: number }
type FirewallStatus = {
  active: boolean
  tool: string
  openPorts: number[]
  openRanges: PortRange[]
}
const firewall = ref<FirewallStatus | null>(null)

async function refreshFirewall() {
  try {
    const r = await http.get<{ success: boolean; obj: FirewallStatus }>(
      'xui/api/firewall-status'
    )
    firewall.value = r.data?.obj ?? null
  } catch {
    firewall.value = null
  }
}

// 当前 inbound 列表里被防火墙挡住的端口。多端口去重并排序,banner
// 文案直接 join 成 "10000, 10002" 这种顺眼的串。端口段也算放行 —
// 单端口或落在任一段 [lo, hi] 内即视为通。
function isOpen(port: number, fw: FirewallStatus): boolean {
  if (fw.openPorts && fw.openPorts.indexOf(port) >= 0) return true
  if (fw.openRanges) {
    for (const r of fw.openRanges) {
      if (port >= r.lo && port <= r.hi) return true
    }
  }
  return false
}

const blockedPorts = computed<number[]>(() => {
  const fw = firewall.value
  if (!fw || !fw.active) return []
  const used = new Set<number>()
  for (const x of inbounds.value) {
    if (typeof x.port === 'number' && !isOpen(x.port, fw)) used.add(x.port)
  }
  return [...used].sort((a, b) => a - b)
})

const totalUp = computed(() => inbounds.value.reduce((a, x) => a + (x.up || 0), 0))
const totalDown = computed(() => inbounds.value.reduce((a, x) => a + (x.down || 0), 0))

const multiUser = computed(() =>
  inbounds.value.filter((x) => isMultiUserProtocol(x.protocol, x.settings))
)
const singleUser = computed(() =>
  inbounds.value.filter((x) => !isMultiUserProtocol(x.protocol, x.settings))
)

// 已占用端口集合,传给 InboundForm 在 add 模式下挑下一个空闲端口,
// 避免新建入站默认 10000 跟现有入站冲突。
const usedPorts = computed(() =>
  inbounds.value.map((x) => x.port).filter((p) => typeof p === 'number')
)

async function reload() {
  loading.value = true
  try {
    const list = await postForm<DBInbound[]>('xui/inbound/list')
    inbounds.value = list || []
  } finally {
    loading.value = false
  }
}

async function refreshOnlineIps() {
  try {
    const r = await postForm<Record<string, string[]>>('xui/inbound/onlineIps')
    onlineIps.value = r || {}
  } catch {
    /* ignore */
  }
}

function onlineCount(tag: string): number {
  const list = onlineIps.value[tag]
  return Array.isArray(list) ? list.length : 0
}

function onlineList(tag: string): string[] {
  return onlineIps.value[tag] || []
}

// ---------- 添加 / 编辑 ----------
const formVisible = ref(false)
const formMode = ref<'add' | 'edit'>('add')
const formInbound = ref<DBInbound | null>(null)

function openAdd() {
  formMode.value = 'add'
  formInbound.value = null
  formVisible.value = true
}

function openEdit(row: DBInbound) {
  formMode.value = 'edit'
  formInbound.value = row
  formVisible.value = true
}

async function onSaved() {
  formVisible.value = false
  await reload()
}

// ---------- 删除 ----------
async function delOne(row: DBInbound) {
  try {
    await ElMessageBox.confirm(`确认删除入站 #${row.id} (${row.remark || row.protocol})?`, '删除', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  await postForm(`xui/inbound/del/${row.id}`)
  ElMessage.success('已删除')
  await reload()
}

async function delAll() {
  if (inbounds.value.length === 0) {
    ElMessage.info('当前没有入站')
    return
  }
  try {
    await ElMessageBox.confirm(
      `确认清空全部 ${inbounds.value.length} 个入站?xray reload 后立即失效,客户端流量记录一并删除,无法恢复。`,
      '一键清空',
      { type: 'error', confirmButtonText: '确认清空', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  await postForm('xui/inbound/del-all')
  ElMessage.success('已清空所有入站')
  await reload()
}

async function toggleEnable(row: DBInbound, val: boolean) {
  try {
    // /xui/inbound/update/:id 走 form-encoded;给整行透传以保持上游兼容
    await postForm(`xui/inbound/update/${row.id}`, { ...row, enable: val })
    row.enable = val
    ElMessage.success(val ? '已启用' : '已禁用')
  } catch {
    row.enable = !val // revert
  }
}

async function resetTraffic(row: DBInbound) {
  try {
    await ElMessageBox.confirm(`重置入站 #${row.id} 的流量?(per-client 流量不会清零)`, '重置流量', {
      type: 'warning',
      confirmButtonText: '重置',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  await postForm(`xui/inbound/update/${row.id}`, { ...row, up: 0, down: 0 })
  ElMessage.success('已重置')
  await reload()
}

// ---------- 客户端流量 modal ----------
const ctVisible = ref(false)
const ctInbound = ref<DBInbound | null>(null)

function openClientTraffic(row: DBInbound) {
  ctInbound.value = row
  ctVisible.value = true
}

// ---------- 单用户详情 modal ----------
// 单用户协议(SS-legacy / Socks / HTTP / Dokodemo)没法走 ClientTrafficModal,
// 也不走多用户的行内二维码;之前 SS-legacy 的"二维码"按钮还因为后端
// LinksByEmail 对无 email 入站返空 map 而点了报错。这里给所有单用户入站
// 一个统一的"连接信息"入口。
const detailsVisible = ref(false)
const detailsInbound = ref<DBInbound | null>(null)

function openDetails(row: DBInbound) {
  detailsInbound.value = row
  detailsVisible.value = true
}

// ---------- lifecycle ----------
onMounted(async () => {
  await reload()
  // 防火墙状态后端 30s cache,前端跟着 inbound list 一起拉一次足够;
  // 用户保存新 inbound 之后在 reload() 里再拉一次,见 onClose() 钩子。
  await Promise.all([refreshOnlineIps(), refreshFirewall(), loadOutbounds()])
  onlineTimer = window.setInterval(() => {
    if (!document.hidden) refreshOnlineIps()
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
  <div class="nx-page">
    <h2>入站列表</h2>

    <!-- 防火墙警告:UFW / firewalld 把当前 inbound 端口拦了,客户端连不上。
         只读检测 + 提示,面板不替用户改防火墙 —— 文案给确切的修复命令,
         多个端口一并展示,不催用户每条单点解决。 -->
    <el-alert
      v-if="blockedPorts.length"
      type="warning"
      :closable="false"
      show-icon
      class="firewall-alert"
    >
      <template #title>
        系统防火墙({{ firewall?.tool?.toUpperCase() }})阻挡入站端口:{{ blockedPorts.join(", ") }}
      </template>
      <template #default>
        <div class="firewall-alert-body">
          客户端从外网连接会超时。在服务器上执行:
          <code class="firewall-alert-cmd">{{
            firewall?.tool === "firewalld"
              ? blockedPorts.map((p) => `firewall-cmd --permanent --add-port=${p}/tcp`).join(" && ") + " && firewall-cmd --reload"
              : blockedPorts.map((p) => `ufw allow ${p}/tcp`).join(" && ")
          }}</code>
        </div>
      </template>
    </el-alert>

    <!-- 总览卡:统计 + 全局操作按钮 -->
    <div class="nx-card overview">
      <div class="overview-stats">
        <div class="stat">
          <span class="stat-label">总上传 / 下载</span>
          <span class="stat-value">{{ sizeFormat(totalUp) }} / {{ sizeFormat(totalDown) }}</span>
        </div>
        <div class="stat">
          <span class="stat-label">总用量</span>
          <span class="stat-value">{{ sizeFormat(totalUp + totalDown) }}</span>
        </div>
        <div class="stat">
          <span class="stat-label">入站数量</span>
          <span class="stat-value">{{ inbounds.length }}</span>
        </div>
      </div>
      <div class="overview-actions">
        <el-button type="primary" :icon="Plus" @click="openAdd">添加入站</el-button>
        <el-button :icon="Refresh" @click="reload" :loading="loading">刷新</el-button>
        <el-button type="danger" :icon="Delete" plain @click="delAll" v-if="inbounds.length > 0">
          清空所有
        </el-button>
      </div>
    </div>

    <!-- 多用户卡片:VLESS / VMess / Trojan / SS-2022 -->
    <el-card class="card-block">
      <template #header>
        <div class="card-head">
          <el-icon class="head-icon"><User /></el-icon>
          <span class="head-title">多用户入站</span>
          <span class="head-sub">VLESS / VMess / Trojan / SS-2022 — 一个端口多个客户</span>
        </div>
      </template>
      <el-empty v-if="multiUser.length === 0" description="还没有多用户入站,点上方「添加入站」创建" />
      <el-table v-else :data="multiUser" stripe size="default">
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-switch :model-value="row.enable" @change="(v: boolean) => toggleEnable(row, v)" />
          </template>
        </el-table-column>
        <el-table-column label="协议" width="100">
          <template #default="{ row }">
            <el-tag>{{ row.protocol }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="port" label="端口" width="90" />
        <el-table-column label="客户数" width="90">
          <template #default="{ row }">
            <el-tag type="info">{{ clientCount(row.settings) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="总流量" width="160">
          <template #default="{ row }">
            <span class="nx-mono">{{ sizeFormat(row.up + row.down) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="出站" width="120">
          <template #default="{ row }">
            <el-tag v-if="row.outboundTag" type="warning" size="small">
              {{ outboundLabel(row.outboundTag) }}
            </el-tag>
            <el-tag v-else type="success" size="small">直连</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="备注" prop="remark" />
        <el-table-column label="操作" width="220" align="right">
          <template #default="{ row }">
            <el-button size="small" type="primary" @click="openClientTraffic(row)">客户端</el-button>
            <el-dropdown trigger="click" @command="(cmd: string) => {
              if (cmd === 'edit') openEdit(row)
              else if (cmd === 'reset') resetTraffic(row)
              else if (cmd === 'del') delOne(row)
            }">
              <el-button size="small">更多 <el-icon><ArrowDown /></el-icon></el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="edit">编辑</el-dropdown-item>
                  <el-dropdown-item command="reset">重置流量(总)</el-dropdown-item>
                  <el-dropdown-item command="del" divided>
                    <span style="color: var(--nx-danger)">删除</span>
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 单用户卡片:SS-legacy / Socks / HTTP / Dokodemo -->
    <el-card class="card-block">
      <template #header>
        <div class="card-head">
          <el-icon class="head-icon"><Connection /></el-icon>
          <span class="head-title">单用户入站</span>
          <span class="head-sub">Shadowsocks legacy / Socks / HTTP / Dokodemo</span>
        </div>
      </template>
      <el-empty v-if="singleUser.length === 0" description="(无单用户入站)" />
      <el-table v-else :data="singleUser" stripe size="default">
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-switch :model-value="row.enable" @change="(v: boolean) => toggleEnable(row, v)" />
          </template>
        </el-table-column>
        <el-table-column label="协议" width="120">
          <template #default="{ row }">
            <el-tag>{{ row.protocol }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="port" label="端口" width="90" />
        <el-table-column label="流量" width="200">
          <template #default="{ row }">
            <span class="nx-mono">{{ sizeFormat(row.up + row.down) }}</span>
            <span v-if="row.total > 0" class="nx-muted"> / {{ sizeFormat(row.total) }}</span>
            <el-tag v-else type="success" size="small" style="margin-left: 6px">无限</el-tag>
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
        <el-table-column label="出站" width="120">
          <template #default="{ row }">
            <el-tag v-if="row.outboundTag" type="warning" size="small">
              {{ outboundLabel(row.outboundTag) }}
            </el-tag>
            <el-tag v-else type="success" size="small">直连</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="在线 IP" width="110">
          <template #default="{ row }">
            <el-tooltip
              v-if="onlineCount(row.tag) > 0"
              effect="light"
              :content="onlineList(row.tag).join('\n')"
            >
              <el-tag type="success">{{ onlineCount(row.tag) }} 在线</el-tag>
            </el-tooltip>
            <el-tag v-else type="info">离线</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="备注" prop="remark" />
        <el-table-column label="操作" width="220" align="right">
          <template #default="{ row }">
            <el-button size="small" type="primary" @click="openDetails(row)">详情</el-button>
            <el-dropdown trigger="click" @command="(cmd: string) => {
              if (cmd === 'edit') openEdit(row)
              else if (cmd === 'reset') resetTraffic(row)
              else if (cmd === 'del') delOne(row)
            }">
              <el-button size="small">更多 <el-icon><ArrowDown /></el-icon></el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="edit">编辑</el-dropdown-item>
                  <el-dropdown-item command="reset">重置流量</el-dropdown-item>
                  <el-dropdown-item command="del" divided>
                    <span style="color: var(--nx-danger)">删除</span>
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <InboundDetailsDialog v-model="detailsVisible" :inbound="detailsInbound" />

    <InboundForm
      v-if="formVisible"
      :mode="formMode"
      :inbound="formInbound"
      :used-ports="usedPorts"
      @close="formVisible = false"
      @saved="onSaved"
    />

    <ClientTrafficModal
      v-if="ctVisible && ctInbound"
      :inbound="ctInbound"
      @close="(reloaded) => { ctVisible = false; if (reloaded) reload() }"
    />
  </div>
</template>

<style scoped>
.overview {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
  padding: 18px 20px;
}

.overview-stats {
  display: flex;
  gap: 32px;
  flex-wrap: wrap;
}

.stat {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.stat-label {
  font-size: 12px;
  color: var(--nx-text-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.stat-value {
  font-size: 18px;
  font-weight: 600;
  color: var(--nx-text-strong);
}

.overview-actions {
  display: flex;
  gap: 8px;
}

.card-block {
  margin-bottom: 16px;
}

.card-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.head-icon {
  color: var(--nx-primary);
}

.head-title {
  font-weight: 600;
  color: var(--nx-text-strong);
}

.head-sub {
  color: var(--nx-text-muted);
  font-size: 12px;
  margin-left: 8px;
}

.firewall-alert {
  margin-bottom: 16px;
}
.firewall-alert-body {
  margin-top: 4px;
  font-size: 13px;
}
.firewall-alert-cmd {
  display: inline-block;
  margin-left: 6px;
  padding: 2px 6px;
  background: rgba(0, 0, 0, 0.06);
  border-radius: 4px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12.5px;
  word-break: break-all;
}
</style>
