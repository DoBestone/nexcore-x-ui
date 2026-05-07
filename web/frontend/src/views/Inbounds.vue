<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { http, post, postForm } from '@/api/http'
import type { DBInbound } from '@/api/types'
import { sizeFormat, fmtTimeMs, isExpired, isMultiUserProtocol, clientCount } from '@/utils/format'
import InboundForm from '@/components/InboundForm.vue'
import ClientTrafficModal from '@/components/ClientTrafficModal.vue'
import QrcodeDialog from '@/components/QrcodeDialog.vue'

const inbounds = ref<DBInbound[]>([])
const loading = ref(false)
const onlineIps = ref<Record<string, string[]>>({})
let onlineTimer: number | null = null

const totalUp = computed(() => inbounds.value.reduce((a, x) => a + (x.up || 0), 0))
const totalDown = computed(() => inbounds.value.reduce((a, x) => a + (x.down || 0), 0))

const multiUser = computed(() =>
  inbounds.value.filter((x) => isMultiUserProtocol(x.protocol, x.settings))
)
const singleUser = computed(() =>
  inbounds.value.filter((x) => !isMultiUserProtocol(x.protocol, x.settings))
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

// ---------- 二维码 ----------
const qrVisible = ref(false)
const qrTitle = ref('')
const qrLink = ref('')

async function showQrcode(row: DBInbound) {
  // 单用户协议:用 inbound 级链接(取第一条 client / 唯一 password)
  // 多用户协议:用 client modal 行内二维码,这里按入站协议判断
  if (!['vmess', 'vless', 'trojan', 'shadowsocks'].includes(row.protocol)) {
    ElMessage.info('该协议不支持分享链接')
    return
  }
  try {
    const links = await http
      .get<{ success: boolean; obj?: Record<string, string> }>(
        `xui/api/inbounds/${row.id}/links`
      )
      .then((r) => r.data?.obj || {})
    const first = Object.values(links)[0]
    if (!first) {
      // SS-legacy 不会有 email,fallback 到 v1 接口需 host;这里直接告知
      ElMessage.info('该入站没有可生成的链接(可能缺少 email/客户)')
      return
    }
    qrTitle.value = row.remark || `inbound#${row.id}`
    qrLink.value = first
    qrVisible.value = true
  } catch (e) {
    ElMessage.error('获取链接失败')
  }
}

// ---------- 客户端流量 modal ----------
const ctVisible = ref(false)
const ctInbound = ref<DBInbound | null>(null)

function openClientTraffic(row: DBInbound) {
  ctInbound.value = row
  ctVisible.value = true
}

// ---------- lifecycle ----------
onMounted(async () => {
  await reload()
  await refreshOnlineIps()
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
        <el-button type="primary" :icon="'Plus'" @click="openAdd">添加入站</el-button>
        <el-button :icon="'Refresh'" @click="reload" :loading="loading">刷新</el-button>
        <el-button type="danger" :icon="'Delete'" plain @click="delAll" v-if="inbounds.length > 0">
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
        <el-table-column label="总流量" width="200">
          <template #default="{ row }">
            <span class="nx-mono">{{ sizeFormat(row.up + row.down) }}</span>
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
            <el-button size="small" @click="showQrcode(row)" v-if="['vmess','vless','trojan','shadowsocks'].includes(row.protocol)">
              二维码
            </el-button>
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

    <InboundForm
      v-if="formVisible"
      :mode="formMode"
      :inbound="formInbound"
      @close="formVisible = false"
      @saved="onSaved"
    />

    <ClientTrafficModal
      v-if="ctVisible && ctInbound"
      :inbound="ctInbound"
      @close="(reloaded) => { ctVisible = false; if (reloaded) reload() }"
    />

    <QrcodeDialog v-model="qrVisible" :title="qrTitle" :link="qrLink" />
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
</style>
