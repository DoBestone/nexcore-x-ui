<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { http, post } from '@/api/http'
import type { ServerStatus } from '@/api/types'
import { sizeFormat, uptimeFormat } from '@/utils/format'

const status = ref<ServerStatus | null>(null)
const versions = ref<string[]>([])
const installing = ref(false)
const restartLoading = ref(false)
const stopLoading = ref(false)
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
  // 强制重启 — 后端 RestartXray(true) 跳过 config-equality 短路。这是
  // 用户主动按按钮的语义:出问题想"再起一次试试",哪怕 config 没变。
  // 失败回显后端 msg(常见:xray 二进制 preflight 拒绝 / config 校验不过)。
  try {
    await ElMessageBox.confirm('确认重启 xray?现有连接会被断开。', '重启 xray', {
      type: 'warning',
      confirmButtonText: '重启',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  restartLoading.value = true
  try {
    await post('xui/api/xray/restart')
    ElMessage.success('xray 已重启')
    await refresh()
  } catch (e: unknown) {
    const msg =
      (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '重启失败'
    ElMessage.error(msg)
  } finally {
    restartLoading.value = false
  }
}

async function stopXray() {
  // 停止后 xray 不会自动起来,除非用户改 inbound config(触发 needRestart
  // cron)或手动按"重启"。所以用 error 级二次确认 — 用户应当清楚"停了之后
  // 整台节点不通"这个语义。
  try {
    await ElMessageBox.confirm(
      '停止 xray 之后,所有客户端立即断流。除非你按"重启"或修改入站配置触发自动重启,xray 不会自己起来。',
      '停止 xray',
      { type: 'error', confirmButtonText: '停止', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  stopLoading.value = true
  try {
    await post('xui/api/xray/stop')
    ElMessage.success('xray 已停止')
    await refresh()
  } catch (e: unknown) {
    const msg =
      (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '停止失败'
    ElMessage.error(msg)
  } finally {
    stopLoading.value = false
  }
}

// ---------- 日志弹层 ----------
const logsVisible = ref(false)
const logsLoading = ref(false)
const logsText = ref('')

async function showLogs() {
  logsVisible.value = true
  logsLoading.value = true
  try {
    const r = await http.get<{ success: boolean; obj?: { logs: string } }>(
      'xui/api/xray/logs'
    )
    logsText.value = r.data?.obj?.logs || '(无日志输出)'
  } catch {
    logsText.value = '(获取日志失败)'
  } finally {
    logsLoading.value = false
  }
}

async function copyLogs() {
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(logsText.value)
      ElMessage.success('已复制')
      return
    }
  } catch {
    /* fallthrough */
  }
  // http 上下文 fallback
  const ta = document.createElement('textarea')
  ta.value = logsText.value
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  const ok = document.execCommand('copy')
  document.body.removeChild(ta)
  ElMessage[ok ? 'success' : 'warning'](ok ? '已复制' : '复制失败')
}

// ---------- 配置弹层 ----------
const configVisible = ref(false)
const configLoading = ref(false)
const configText = ref('')

async function showConfig() {
  configVisible.value = true
  configLoading.value = true
  try {
    // /xui/api/xray/config 直接返回 JSON 字符串(非 jsonObj 包装),
    // 用 http.get 拿原文,Content-Type 是 application/json 但 axios
    // 会自动 parse — 这里强制 transformResponse 拿原始字符串,
    // 否则 textarea 显示 [object Object]。
    const r = await http.get<string>('xui/api/xray/config', {
      transformResponse: [(data: unknown) => data as string]
    })
    configText.value = typeof r.data === 'string' ? r.data : JSON.stringify(r.data, null, 2)
  } catch {
    configText.value = '(获取配置失败)'
  } finally {
    configLoading.value = false
  }
}

async function copyConfig() {
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(configText.value)
      ElMessage.success('已复制')
      return
    }
  } catch {
    /* fallthrough */
  }
  const ta = document.createElement('textarea')
  ta.value = configText.value
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  const ok = document.execCommand('copy')
  document.body.removeChild(ta)
  ElMessage[ok ? 'success' : 'warning'](ok ? '已复制' : '复制失败')
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

      <!-- 项目信息卡:版本号 + 开源仓库链接。GitHub URL 硬编码,版本号从
           /server/status 拿(后端从 config/version 嵌入,build 时同步)。
           不调 GitHub API 列 stars/issues — 那要 CORS + token,网速一抖
           整个 Dashboard 加载就被拖,不值得。 -->
      <div class="nx-card project">
        <div class="card-title">项目信息</div>
        <div class="kv">
          <span>面板版本</span>
          <b>{{ status?.panelVersion ? 'v' + status.panelVersion : '-' }}</b>
        </div>
        <div class="kv">
          <span>开源仓库</span>
          <b>
            <a
              href="https://github.com/DoBestone/nexcore-x-ui"
              target="_blank"
              rel="noopener noreferrer"
              class="repo-link"
            >DoBestone/nexcore-x-ui ↗</a>
          </b>
        </div>
        <div class="muted project-foot">
          MIT 协议,issue / PR 欢迎。
        </div>
      </div>

      <!-- 项目方商业站点 — NexCore 同品牌的 9188.pro 综合数字基础服务平台
           (VPS / 域名 / 主机托管)。链接外开,不写访客追踪参数。
           保持中性文案,不喊"立即抢购"。 -->
      <div class="nx-card sponsor">
        <div class="card-title">推荐 · NexCore</div>
        <div class="sponsor-tag">VPS · 域名 · 主机托管</div>
        <div class="muted">由项目方运营的综合数字基础服务平台,与本面板同品牌。</div>
        <a
          href="https://9188.pro/?utm_source=nexcore-panel&utm_medium=dashboard"
          target="_blank"
          rel="noopener noreferrer"
          class="sponsor-cta"
        >访问 9188.pro ↗</a>
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
        </div>
        <div v-if="status?.xray.errorMsg" class="xray-err">
          {{ status.xray.errorMsg }}
        </div>
        <div class="xray-actions">
          <el-button
            type="primary"
            size="small"
            :loading="restartLoading"
            :disabled="stopLoading"
            @click="restartXray"
          >重启 xray</el-button>
          <el-button
            type="danger"
            size="small"
            plain
            :loading="stopLoading"
            :disabled="restartLoading || status?.xray.state !== 'running'"
            @click="stopXray"
          >停止</el-button>
          <el-button size="small" @click="showLogs">查看日志</el-button>
          <el-button size="small" @click="showConfig">查看配置</el-button>
          <el-button size="small" @click="refresh">刷新状态</el-button>
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

    <!-- xray 日志弹层 -->
    <el-dialog
      v-model="logsVisible"
      title="xray 日志"
      width="720px"
      class="constrained-dialog"
      :align-center="true"
    >
      <div v-loading="logsLoading">
        <el-input
          v-model="logsText"
          type="textarea"
          :rows="18"
          readonly
          class="nx-mono-textarea"
        />
        <div class="dialog-foot-hint">
          <span class="muted">最近 ~100 行 stdout/stderr,重启后会清空</span>
        </div>
      </div>
      <template #footer>
        <el-button @click="showLogs" :loading="logsLoading">刷新</el-button>
        <el-button type="primary" @click="copyLogs">复制</el-button>
        <el-button @click="logsVisible = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- xray 生效配置弹层 -->
    <el-dialog
      v-model="configVisible"
      title="xray 当前配置"
      width="720px"
      class="constrained-dialog"
      :align-center="true"
    >
      <div v-loading="configLoading">
        <el-input
          v-model="configText"
          type="textarea"
          :rows="18"
          readonly
          class="nx-mono-textarea"
        />
        <div class="dialog-foot-hint">
          <span class="muted">面板根据当前模板 + 入站列表渲染出的 JSON;实际 xray 是否已加载这份配置取决于最近一次重启</span>
        </div>
      </div>
      <template #footer>
        <el-button type="primary" @click="copyConfig">复制</el-button>
        <el-button @click="configVisible = false">关闭</el-button>
      </template>
    </el-dialog>
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

/* 项目信息卡:GitHub 链接保持文字色 + 下划线悬浮,跟内容卡同视觉权重 */
.repo-link {
  color: var(--nx-primary);
  text-decoration: none;
  font-weight: 500;
}
.repo-link:hover {
  text-decoration: underline;
}
.project-foot {
  margin-top: 8px;
  font-size: 12px;
}

/* 商业推荐卡:稍微突出但不喧宾,渐变边色区分 system 卡 + 内容卡 */
.sponsor {
  background: linear-gradient(135deg, #f5f8ff 0%, #ffffff 100%);
  border: 1px solid #dfe7ff;
}
.sponsor-tag {
  font-size: 13px;
  color: var(--nx-text-strong);
  font-weight: 600;
  margin-bottom: 6px;
}
.sponsor-cta {
  display: inline-block;
  margin-top: 10px;
  padding: 6px 14px;
  background: var(--nx-primary);
  color: #fff;
  border-radius: 6px;
  font-size: 13px;
  text-decoration: none;
  transition: background 0.15s;
}
.sponsor-cta:hover {
  background: #1d4ed8;
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
.xray-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
}
.dialog-foot-hint {
  margin-top: 8px;
  font-size: 12px;
}
:deep(.nx-mono-textarea .el-textarea__inner) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre;
  overflow-x: auto;
}
</style>
