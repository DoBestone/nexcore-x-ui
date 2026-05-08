<script setup lang="ts">
// 系统更新 — 一站式查看 / 触发面板自更新 + 浏览历史更新日志。
//
// 三块:
//   1. 当前版本 vs 最新版本卡片;有新版时给红点 + 详情。
//   2. 立即更新按钮 → 触发后端 ApplyLatest;在更新过程中轮询
//      /xui/api/update/progress,把阶段(下载/校验/解压/安装/重启)
//      展示成 step。再 exec 之后后端短暂不可达,前端按 "连接被拒
//      ≥ 2s" 判断已重启,提示用户刷新。
//   3. 历史更新日志 — 拉 /xui/api/update/releases?limit=30,marked 渲染
//      release body。同 ApiConsole 的做法,源是上游仓库自己的 release,
//      可信。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  RefreshRight,
  CircleCheck,
  Warning,
  Download,
  Loading,
  CaretRight,
  Document
} from '@element-plus/icons-vue'
import { Marked } from 'marked'
import { get, post } from '@/api/http'

interface ReleaseAsset {
  name: string
  browser_download_url: string
  size: number
}
interface ReleaseInfo {
  tag_name: string
  name: string
  body: string
  published_at: string
  assets: ReleaseAsset[]
}
interface UpdateCheck {
  current: string
  latest: string
  updateAvailable: boolean
  release: ReleaseInfo | null
}
interface ApplyProgress {
  state:
    | 'idle'
    | 'downloading'
    | 'verifying'
    | 'extracting'
    | 'installing'
    | 'restarting'
    | 'done'
    | 'error'
  message: string
  targetTag?: string
  startedAt?: number
  updatedAt?: number
  error?: string
  currentVersion?: string
}

const md = new Marked({ gfm: true, breaks: false })

const checking = ref(false)
const applying = ref(false)
const check = ref<UpdateCheck | null>(null)
const progress = ref<ApplyProgress | null>(null)
const releases = ref<ReleaseInfo[]>([])
const releasesLoading = ref(false)
const releasesError = ref('')

// 升级流程 step 顺序 — 跟后端 ApplyState* 常量一一对应。
const phases: Array<{ key: ApplyProgress['state']; label: string }> = [
  { key: 'downloading', label: '下载' },
  { key: 'verifying', label: '校验' },
  { key: 'extracting', label: '解压' },
  { key: 'installing', label: '安装' },
  { key: 'restarting', label: '重启' }
]

const phaseIndex = computed(() => {
  if (!progress.value) return -1
  const idx = phases.findIndex((p) => p.key === progress.value!.state)
  return idx
})

let progressTimer: number | null = null
let restartProbeStart = 0

function stopProgressPoll() {
  if (progressTimer) {
    clearInterval(progressTimer)
    progressTimer = null
  }
}

async function pollProgress() {
  try {
    const p = await get<ApplyProgress>('xui/api/update/progress')
    progress.value = p
    if (p.state === 'error') {
      stopProgressPoll()
      applying.value = false
      ElMessage.error(p.error || '升级失败')
    }
  } catch (e: unknown) {
    // 在 restarting 阶段后端会断开 — 这里把"连不上"当成"已重启"
    // 的信号:连续 2s 拿不到响应就提示用户刷新页面。
    if (progress.value?.state === 'restarting') {
      if (restartProbeStart === 0) restartProbeStart = Date.now()
      if (Date.now() - restartProbeStart > 2000) {
        stopProgressPoll()
        applying.value = false
        progress.value = {
          ...(progress.value as ApplyProgress),
          state: 'done',
          message: '升级完成,新进程已启动'
        }
        ElMessageBox.confirm(
          '面板已升级并重启完成。点击"刷新"重新加载页面以使用新版本。',
          '升级完成',
          { type: 'success', confirmButtonText: '刷新', cancelButtonText: '稍后' }
        )
          .then(() => window.location.reload())
          .catch(() => {})
      }
    }
  }
}

async function doCheck(silent = false) {
  checking.value = true
  try {
    check.value = await get<UpdateCheck>('xui/api/update/check')
    if (!silent) {
      if (check.value?.updateAvailable) {
        ElMessage.success(
          `发现新版本 ${check.value.latest}(当前 ${check.value.current})`
        )
      } else {
        ElMessage.info(
          `已是最新版本(${check.value?.current || '未知'})`
        )
      }
    }
  } catch (e: unknown) {
    const msg =
      (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg ||
      '查询失败'
    if (!silent) ElMessage.error(msg)
  } finally {
    checking.value = false
  }
}

async function doApply(targetVersion?: string) {
  const target = targetVersion || check.value?.latest || ''
  try {
    await ElMessageBox.confirm(
      `将下载并安装 ${target || '最新'} 版本。安装完成后面板会自动重启,期间约 5–15 秒不可访问。继续?`,
      '在线更新',
      { type: 'warning', confirmButtonText: '立即更新', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  applying.value = true
  restartProbeStart = 0
  progress.value = {
    state: 'downloading',
    message: '正在准备…',
    targetTag: target
  }
  // 先开轮询,再发请求 — apply 接口是阻塞的,响应可能迟迟回不来,
  // 但后端在每个阶段都会更新 progress,前端轮询能实时反映。
  progressTimer = window.setInterval(pollProgress, 800)
  try {
    await post('xui/api/update/apply', target ? { version: target } : undefined)
  } catch (e: unknown) {
    // 不在这里关 applying:可能是 re-exec 已开始,axios 看到的是
    // 「连接断开」。把后续判定交给 pollProgress() 的 restart 探测。
    if (progress.value?.state !== 'restarting') {
      stopProgressPoll()
      applying.value = false
      const msg =
        (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg ||
        '升级失败'
      ElMessage.error(msg)
    }
  }
}

async function loadReleases() {
  releasesLoading.value = true
  releasesError.value = ''
  try {
    releases.value = (await get<ReleaseInfo[]>('xui/api/update/releases?limit=30')) || []
  } catch (e: unknown) {
    releasesError.value =
      (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg ||
      '获取更新日志失败'
  } finally {
    releasesLoading.value = false
  }
}

function fmtDate(s: string): string {
  if (!s) return '-'
  const d = new Date(s)
  if (isNaN(d.getTime())) return s
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

function renderBody(body: string): string {
  if (!body) return '<p class="muted">(无更新说明)</p>'
  return md.parse(body) as string
}

function isLatestRelease(r: ReleaseInfo): boolean {
  return !!check.value?.latest && r.tag_name === check.value.latest
}

function isCurrentRelease(r: ReleaseInfo): boolean {
  if (!check.value?.current) return false
  return (
    r.tag_name === check.value.current ||
    r.tag_name === 'v' + check.value.current ||
    'v' + r.tag_name === check.value.current
  )
}

onMounted(async () => {
  await Promise.all([doCheck(true), loadReleases()])
})

onBeforeUnmount(() => {
  stopProgressPoll()
})
</script>

<template>
  <div class="nx-page">
    <h2>系统更新</h2>
    <p class="muted page-sub">
      在线下载并安装最新面板版本。所有更新包均经 SHA256 校验,失败会自动回滚。
    </p>

    <!-- 顶部版本对比卡 -->
    <div class="version-card">
      <div class="ver-cell">
        <div class="ver-label">当前版本</div>
        <div class="ver-value">v{{ check?.current || '-' }}</div>
      </div>
      <el-icon class="ver-arrow"><CaretRight /></el-icon>
      <div class="ver-cell">
        <div class="ver-label">最新版本</div>
        <div class="ver-value">
          <template v-if="check?.latest">{{ check.latest }}</template>
          <template v-else>查询中…</template>
          <el-tag
            v-if="check?.updateAvailable"
            type="warning"
            size="small"
            effect="dark"
            class="ver-tag"
          >
            <el-icon><Warning /></el-icon>
            有新版本
          </el-tag>
          <el-tag
            v-else-if="check && !check.updateAvailable"
            type="success"
            size="small"
            effect="plain"
            class="ver-tag"
          >
            <el-icon><CircleCheck /></el-icon>
            已是最新
          </el-tag>
        </div>
      </div>
      <div class="ver-actions">
        <el-button :icon="RefreshRight" :loading="checking" @click="doCheck(false)">
          检查更新
        </el-button>
        <el-button
          type="primary"
          :icon="Download"
          :disabled="!check?.updateAvailable || applying"
          :loading="applying"
          @click="doApply()"
        >
          立即更新
        </el-button>
      </div>
    </div>

    <!-- 升级进度卡 — 进行中才显示 -->
    <div v-if="applying || progress?.state === 'error'" class="progress-card">
      <div class="progress-title">
        <el-icon class="is-loading" v-if="applying"><Loading /></el-icon>
        <el-icon v-else style="color: var(--nx-danger)"><Warning /></el-icon>
        <span v-if="applying">正在升级到 {{ progress?.targetTag || '...' }}</span>
        <span v-else style="color: var(--nx-danger)">升级失败</span>
      </div>
      <el-steps
        v-if="progress?.state !== 'error'"
        :active="phaseIndex < 0 ? 0 : phaseIndex"
        :process-status="progress?.state === 'restarting' ? 'success' : 'process'"
        finish-status="success"
        align-center
        class="phase-steps"
      >
        <el-step v-for="p in phases" :key="p.key" :title="p.label" />
      </el-steps>
      <div class="progress-msg">
        {{ progress?.message || '...' }}
      </div>
      <div v-if="progress?.error" class="progress-err">
        {{ progress.error }}
      </div>
    </div>

    <!-- 当前版本说明 (有新版且 release 有 body 时) -->
    <div v-if="check?.release?.body" class="latest-note">
      <div class="note-title">
        <el-icon><Document /></el-icon>
        <span>{{ check.latest }} 更新说明</span>
        <span class="muted note-date">{{ fmtDate(check.release.published_at) }}</span>
      </div>
      <div class="note-body md-body" v-html="renderBody(check.release.body)" />
    </div>

    <!-- 历史更新日志 -->
    <div class="changelog">
      <div class="changelog-head">
        <h3>更新日志</h3>
        <el-button text :icon="RefreshRight" :loading="releasesLoading" @click="loadReleases">
          刷新
        </el-button>
      </div>
      <div v-if="releasesError" class="changelog-err">
        {{ releasesError }}
      </div>
      <el-skeleton v-else-if="releasesLoading && releases.length === 0" :rows="6" animated />
      <div v-else-if="releases.length === 0" class="muted">暂无历史 release。</div>
      <el-collapse v-else accordion>
        <el-collapse-item
          v-for="r in releases"
          :key="r.tag_name"
          :name="r.tag_name"
        >
          <template #title>
            <div class="rel-title">
              <span class="rel-tag">{{ r.tag_name }}</span>
              <span class="rel-name">{{ r.name || '' }}</span>
              <span class="rel-date muted">{{ fmtDate(r.published_at) }}</span>
              <el-tag v-if="isCurrentRelease(r)" type="info" size="small" effect="plain">
                当前
              </el-tag>
              <el-tag v-else-if="isLatestRelease(r)" type="warning" size="small" effect="dark">
                最新
              </el-tag>
            </div>
          </template>
          <div class="md-body" v-html="renderBody(r.body)" />
          <div class="rel-actions">
            <el-button
              v-if="!isCurrentRelease(r)"
              size="small"
              type="primary"
              :icon="Download"
              :disabled="applying"
              @click="doApply(r.tag_name)"
            >
              安装此版本
            </el-button>
            <span v-else class="muted">这是当前正在运行的版本。</span>
          </div>
        </el-collapse-item>
      </el-collapse>
    </div>
  </div>
</template>

<style scoped>
.page-sub {
  margin-top: -8px;
  margin-bottom: 16px;
}

.version-card {
  background: #fff;
  border: 1px solid var(--nx-border);
  border-radius: 12px;
  padding: 18px 20px;
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
}

.ver-cell {
  flex: 0 0 auto;
  min-width: 140px;
}

.ver-label {
  font-size: 12px;
  color: var(--nx-text-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin-bottom: 4px;
}

.ver-value {
  font-size: 22px;
  font-weight: 600;
  color: var(--nx-text-strong);
  display: flex;
  align-items: center;
  gap: 8px;
}

.ver-arrow {
  color: var(--nx-text-muted);
  font-size: 20px;
}

.ver-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
}

.ver-actions {
  margin-left: auto;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.progress-card {
  background: #fff;
  border: 1px solid var(--nx-border);
  border-radius: 12px;
  padding: 18px 20px;
  margin-bottom: 16px;
}

.progress-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  color: var(--nx-text-strong);
  margin-bottom: 14px;
}

.phase-steps {
  margin-bottom: 12px;
}

.progress-msg {
  font-size: 13px;
  color: var(--nx-text-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.progress-err {
  margin-top: 8px;
  background: #fef2f2;
  color: var(--nx-danger);
  padding: 8px 12px;
  border-radius: 8px;
  font-size: 12.5px;
  white-space: pre-wrap;
  word-break: break-all;
}

.latest-note {
  background: linear-gradient(135deg, #f5f8ff 0%, #ffffff 100%);
  border: 1px solid #dfe7ff;
  border-radius: 12px;
  padding: 18px 22px;
  margin-bottom: 16px;
}

.note-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  color: var(--nx-text-strong);
  margin-bottom: 10px;
}

.note-date {
  margin-left: auto;
  font-weight: 400;
  font-size: 12.5px;
}

.changelog {
  background: #fff;
  border: 1px solid var(--nx-border);
  border-radius: 12px;
  padding: 14px 18px 18px;
}

.changelog-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 4px;
}

.changelog-head h3 {
  margin: 0;
  font-size: 16px;
  color: var(--nx-text-strong);
}

.changelog-err {
  background: #fef2f2;
  color: var(--nx-danger);
  padding: 8px 12px;
  border-radius: 8px;
  font-size: 13px;
}

.rel-title {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  flex-wrap: wrap;
}

.rel-tag {
  font-weight: 600;
  color: var(--nx-primary);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.rel-name {
  color: var(--nx-text-strong);
}

.rel-date {
  margin-left: auto;
  font-size: 12.5px;
}

.rel-actions {
  margin-top: 12px;
  display: flex;
  align-items: center;
  gap: 10px;
}

/* markdown 输出公共样式 — 跟 ApiConsole 一致即可 */
.md-body :deep(h1),
.md-body :deep(h2),
.md-body :deep(h3) {
  margin-top: 14px;
  margin-bottom: 8px;
  color: var(--nx-text-strong);
}
.md-body :deep(h1) {
  font-size: 18px;
}
.md-body :deep(h2) {
  font-size: 16px;
}
.md-body :deep(h3) {
  font-size: 14px;
}
.md-body :deep(p) {
  margin: 6px 0;
  line-height: 1.6;
  font-size: 13.5px;
}
.md-body :deep(ul),
.md-body :deep(ol) {
  padding-left: 22px;
  margin: 6px 0;
  font-size: 13.5px;
}
.md-body :deep(li) {
  margin: 2px 0;
  line-height: 1.6;
}
.md-body :deep(code) {
  background: #f1f3f5;
  padding: 1px 5px;
  border-radius: 4px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12.5px;
}
.md-body :deep(pre) {
  background: #f8f9fa;
  border: 1px solid var(--nx-border);
  padding: 10px 12px;
  border-radius: 8px;
  overflow-x: auto;
  font-size: 12.5px;
}
.md-body :deep(pre code) {
  background: transparent;
  padding: 0;
}
.md-body :deep(a) {
  color: var(--nx-primary);
  text-decoration: none;
}
.md-body :deep(a:hover) {
  text-decoration: underline;
}
.md-body :deep(blockquote) {
  border-left: 3px solid var(--nx-border);
  padding: 4px 12px;
  color: var(--nx-text-muted);
  margin: 8px 0;
}
</style>
