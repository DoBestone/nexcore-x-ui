<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { post } from '@/api/http'
import type { ServerStatus } from '@/api/types'
import { sizeFormat, uptimeFormat } from '@/utils/format'

const status = ref<ServerStatus | null>(null)
const versions = ref<string[]>([])
const installing = ref(false)
const restartLoading = ref(false)
let timer: number | null = null

async function refresh() {
  try {
    const s = await post<ServerStatus>('server/status')
    status.value = s
  } catch {
    /* 网络抖动忽略 */
  }
}

async function loadVersions() {
  try {
    versions.value = (await post<string[]>('server/getXrayVersion')) || []
  } catch {
    /* ignore */
  }
}

async function installXray(version: string) {
  try {
    await ElMessageBox.confirm(`确认安装 xray ${version}?`, '安装 xray', {
      type: 'warning',
      confirmButtonText: '安装',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  installing.value = true
  try {
    await post(`server/installXray/${encodeURIComponent(version)}`)
    ElMessage.success('已安装,正在重启 xray')
  } finally {
    installing.value = false
    await refresh()
  }
}

async function restartXray() {
  restartLoading.value = true
  try {
    // 没有专门 restart 路由,改一个全局设置 PUT 触发 needRestart 不合适;
    // 用 setting/restartPanel 走的是面板重启,xray 由后端 cron 跟踪
    // needRestart 自动重启。这里直接刷状态即可。
    await refresh()
    ElMessage.info('xray 状态已刷新(变更配置后 ~10s 内生效)')
  } finally {
    restartLoading.value = false
  }
}

onMounted(async () => {
  await Promise.all([refresh(), loadVersions()])
  timer = window.setInterval(refresh, 2000)
})

onBeforeUnmount(() => {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
})
</script>

<template>
  <div class="nx-page">
    <h2>系统状态</h2>

    <div class="grid">
      <div class="nx-card">
        <div class="card-title">CPU</div>
        <div class="card-big">{{ status ? status.cpu.toFixed(1) : '-' }}<span>%</span></div>
        <el-progress
          :percentage="Math.min(100, status?.cpu || 0)"
          :stroke-width="6"
          :show-text="false"
          :color="(status?.cpu || 0) > 80 ? '#b91c1c' : '#2563eb'"
        />
      </div>

      <div class="nx-card">
        <div class="card-title">内存</div>
        <div class="card-big">
          {{ status ? sizeFormat(status.mem.current) : '-' }}
          <span class="muted-of">/ {{ status ? sizeFormat(status.mem.total) : '-' }}</span>
        </div>
        <el-progress
          :percentage="
            status?.mem.total ? Math.round((status.mem.current * 100) / status.mem.total) : 0
          "
          :stroke-width="6"
          :show-text="false"
        />
      </div>

      <div class="nx-card">
        <div class="card-title">磁盘</div>
        <div class="card-big">
          {{ status ? sizeFormat(status.disk.current) : '-' }}
          <span class="muted-of">/ {{ status ? sizeFormat(status.disk.total) : '-' }}</span>
        </div>
        <el-progress
          :percentage="
            status?.disk.total ? Math.round((status.disk.current * 100) / status.disk.total) : 0
          "
          :stroke-width="6"
          :show-text="false"
        />
      </div>

      <div class="nx-card">
        <div class="card-title">运行时长</div>
        <div class="card-big">{{ status ? uptimeFormat(status.uptime) : '-' }}</div>
        <div class="muted">负载 {{ status ? status.loads.map((x) => x.toFixed(2)).join(' / ') : '-' }}</div>
      </div>

      <div class="nx-card">
        <div class="card-title">网络速率</div>
        <div class="kv">
          <span>↑ 上行</span>
          <b>{{ status ? sizeFormat(status.netIO.up) + '/s' : '-' }}</b>
        </div>
        <div class="kv">
          <span>↓ 下行</span>
          <b>{{ status ? sizeFormat(status.netIO.down) + '/s' : '-' }}</b>
        </div>
        <div class="kv">
          <span>TCP / UDP</span>
          <b>{{ status?.tcpCount || 0 }} / {{ status?.udpCount || 0 }}</b>
        </div>
      </div>

      <div class="nx-card">
        <div class="card-title">累计流量</div>
        <div class="kv">
          <span>已发送</span>
          <b>{{ status ? sizeFormat(status.netTraffic.sent) : '-' }}</b>
        </div>
        <div class="kv">
          <span>已接收</span>
          <b>{{ status ? sizeFormat(status.netTraffic.recv) : '-' }}</b>
        </div>
      </div>

      <div class="nx-card xray">
        <div class="card-title">Xray</div>
        <div class="xray-row">
          <el-tag
            :type="
              status?.xray.state === 'running'
                ? 'success'
                : status?.xray.state === 'error'
                  ? 'danger'
                  : 'info'
            "
            size="large"
          >
            {{ status?.xray.state || '-' }}
          </el-tag>
          <span class="nx-mono">{{ status?.xray.version || '-' }}</span>
          <el-button size="small" @click="restartXray" :loading="restartLoading">刷新</el-button>
        </div>
        <div v-if="status?.xray.errorMsg" class="xray-err">
          {{ status.xray.errorMsg }}
        </div>
        <div class="versions" v-if="versions.length">
          <div class="muted">可用版本:</div>
          <div class="version-list">
            <el-button
              v-for="v in versions.slice(0, 8)"
              :key="v"
              size="small"
              :loading="installing"
              @click="installXray(v)"
            >
              {{ v }}
            </el-button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 16px;
}

.card-title {
  color: var(--nx-text-muted);
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin-bottom: 8px;
}

.card-big {
  font-size: 22px;
  font-weight: 600;
  color: var(--nx-text-strong);
  margin-bottom: 12px;
}

.card-big span {
  font-size: 14px;
  color: var(--nx-text-muted);
  margin-left: 4px;
}

.card-big .muted-of {
  font-weight: 400;
}

.kv {
  display: flex;
  justify-content: space-between;
  padding: 4px 0;
  border-bottom: 1px dashed var(--nx-border);
}
.kv:last-child {
  border-bottom: none;
}
.kv b {
  color: var(--nx-text-strong);
}

.muted {
  color: var(--nx-text-muted);
  font-size: 12.5px;
}

.xray {
  grid-column: span 2;
}
.xray-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
}
.xray-err {
  color: var(--nx-danger);
  background: #fef2f2;
  padding: 8px 12px;
  border-radius: 8px;
  font-family: ui-monospace, monospace;
  font-size: 12px;
  margin-bottom: 8px;
  white-space: pre-wrap;
  word-break: break-all;
}
.versions {
  margin-top: 12px;
}
.version-list {
  margin-top: 6px;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
</style>
