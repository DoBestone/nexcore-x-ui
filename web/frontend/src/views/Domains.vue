<script setup lang="ts">
// 一键域名绑定向导。整个流程后端 DomainBindingService 跑完整链路:
//   预检环境 → 申请证书 → 写 nginx → 创建 xray 入站 → reload。
// 前端只负责收集输入 + 展示报告 + 展示日志。
//
// 流程节点:
//   1. 进页面立刻 GET /xui/domain/detect 拿环境报告;红色 issue 用户先解决
//   2. 用户填表 → POST /xui/domain/bind → 后端跑 1-3 分钟(取证书最慢)
//   3. 完成弹分享链接 + 二维码 + 完整 xray 配置预览
import { onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Lock } from '@element-plus/icons-vue'
import { post, get } from '@/api/http'

interface EnvReport {
  panelIsRoot: boolean
  nginxInstalled: boolean
  nginxVersion: string
  nginxRunning: boolean
  nginxConfDir: string
  port80Free: boolean
  port443Free: boolean
  port80Owner?: string
  port443Owner?: string
  acmeShInstalled: boolean
  acmeShPath?: string
  certbotInstalled: boolean
  packageManager: string
  issues: string[]
}

interface BindResult {
  success: boolean
  error?: string
  logs: string[]
  domain?: string
  nginxConfPath?: string
  inboundId?: number
  wsPath?: string
  xrayPort?: number
}

interface CFRecordState {
  domain: string
  zoneId: string
  zoneName: string
  recordId: string
  type: string
  content: string
  proxied: boolean
}

const env = ref<EnvReport | null>(null)
const detecting = ref(false)
const binding = ref(false)
const result = ref<BindResult | null>(null)

async function detect() {
  detecting.value = true
  try {
    env.value = await get<EnvReport>('xui/domain/detect')
  } catch (e: unknown) {
    const msg = (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '检测失败'
    ElMessage.error(msg)
  } finally {
    detecting.value = false
  }
}

const form = ref({
  domain: '',
  email: '',
  acmeMode: 'dns01-cf' as 'http01' | 'dns01-cf',
  cfApiToken: '',
  installMissing: true
})

async function doBind() {
  if (!form.value.domain || !form.value.email) {
    ElMessage.warning('域名 / email 必填')
    return
  }
  if (form.value.acmeMode === 'dns01-cf' && !form.value.cfApiToken) {
    ElMessage.warning('DNS-01 模式需要填 CF API token')
    return
  }
  binding.value = true
  result.value = null
  try {
    const r = await post<BindResult>('xui/domain/bind', form.value)
    result.value = r
    if (r.success) {
      ElMessage.success('绑定完成,已写入 nginx 并创建入站')
    } else {
      ElMessage.error(r.error || '绑定失败,见下方日志')
    }
  } catch (e: unknown) {
    const msg = (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '请求失败'
    ElMessage.error(msg)
  } finally {
    binding.value = false
  }
}

// ---------- CF 代理状态 ----------
// CF API 控制层 — 看 / 切某个域名的 proxied 字段(橙云/灰云)。token 在
// 面板设置里配过的话,这里就能用。token 缺失或权限不够时给明确报错。
const cfDomain = ref('')
const cfState = ref<CFRecordState | null>(null)
const cfLoading = ref(false)
const cfToggling = ref(false)
const cfError = ref('')

async function cfRefresh() {
  if (!cfDomain.value.trim()) return
  cfLoading.value = true
  cfError.value = ''
  cfState.value = null
  try {
    cfState.value = await get<CFRecordState>(
      `xui/domain/cf-status?domain=${encodeURIComponent(cfDomain.value.trim())}`
    )
  } catch (e: unknown) {
    cfError.value =
      (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '查询失败'
  } finally {
    cfLoading.value = false
  }
}

// 解绑 — 删 nginx 配置 + reload。xray 入站不动(里面可能还有客户),
// 让用户决定要不要删。二次确认必须的:误点会立刻断掉 CF 边缘 → nginx
// 这条转发链,所有走该域名的客户端瞬间掉线。
const unbinding = ref(false)
async function doUnbind() {
  if (!result.value?.domain) return
  const domain = result.value.domain
  try {
    await ElMessageBox.confirm(
      `确认解绑域名 ${domain}?会删除 /etc/nginx/conf.d/nx-${domain}.conf 并 reload nginx,
所有走该域名的客户端会立刻断开。xray 入站不会被删,你想清掉它得去入站列表手动删。`,
      '解绑域名',
      {
        type: 'warning',
        confirmButtonText: '解绑',
        cancelButtonText: '取消'
      }
    )
  } catch {
    return
  }
  unbinding.value = true
  try {
    await post('xui/domain/unbind', { domain })
    ElMessage.success(`已解绑 ${domain}(nginx 已 reload,xray 入站需手动清理)`)
    result.value = null
  } catch (e: unknown) {
    const msg = (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '解绑失败'
    ElMessage.error(msg)
  } finally {
    unbinding.value = false
  }
}

async function cfToggle(target: boolean) {
  if (!cfState.value) return
  cfToggling.value = true
  try {
    cfState.value = await post<CFRecordState>('xui/domain/cf-toggle', {
      domain: cfState.value.domain,
      proxied: target
    })
    ElMessage.success(target ? '已切换为橙云(代理开启)' : '已切换为灰云(DNS-only)')
  } catch (e: unknown) {
    const msg = (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '切换失败'
    ElMessage.error(msg)
  } finally {
    cfToggling.value = false
  }
}

// 绑定成功后把当前域名带过去做初始查询
watch(
  () => result.value?.domain,
  (d) => {
    if (d) {
      cfDomain.value = d
      cfRefresh()
    }
  }
)

onMounted(detect)
</script>

<template>
  <div class="nx-page">
    <h2>域名绑定(一键 CF + nginx + xray)</h2>

    <p class="nx-muted" style="margin: 0 0 16px 0; font-size: 13px;">
      把面板的 vless+ws 入站挂到你的域名 + nginx + Let's Encrypt 证书后面,
      让客户端走 <code>example.com:443</code> → CF 边缘 → 你的 nginx → xray,
      origin IP 全程不暴露。
    </p>

    <!-- 环境检测 -->
    <el-card style="margin-bottom: 16px">
      <template #header>
        <div style="display: flex; align-items: center; justify-content: space-between;">
          <span style="font-weight: 600">环境预检</span>
          <el-button size="small" :icon="Refresh" :loading="detecting" @click="detect">重新检测</el-button>
        </div>
      </template>
      <div v-if="env" class="env-grid">
        <div class="env-row">
          <span class="env-label">面板 root 权限</span>
          <el-tag :type="env.panelIsRoot ? 'success' : 'danger'">
            {{ env.panelIsRoot ? '是' : '否(必须 root)' }}
          </el-tag>
        </div>
        <div class="env-row">
          <span class="env-label">nginx</span>
          <el-tag :type="env.nginxInstalled ? 'success' : 'danger'">
            {{ env.nginxInstalled ? env.nginxVersion : '未安装' }}
          </el-tag>
          <el-tag v-if="env.nginxInstalled" :type="env.nginxRunning ? 'success' : 'warning'" size="small">
            {{ env.nginxRunning ? 'running' : 'stopped' }}
          </el-tag>
        </div>
        <div class="env-row">
          <span class="env-label">acme.sh / certbot</span>
          <el-tag :type="env.acmeShInstalled ? 'success' : 'info'">
            acme.sh: {{ env.acmeShInstalled ? '已装' : '未装' }}
          </el-tag>
          <el-tag :type="env.certbotInstalled ? 'success' : 'info'" size="small">
            certbot: {{ env.certbotInstalled ? '已装' : '未装' }}
          </el-tag>
        </div>
        <div class="env-row">
          <span class="env-label">80 端口</span>
          <el-tag :type="env.port80Free ? 'success' : 'warning'">
            {{ env.port80Free ? '空闲' : '被 ' + (env.port80Owner || '?') + ' 占用' }}
          </el-tag>
        </div>
        <div class="env-row">
          <span class="env-label">443 端口</span>
          <el-tag :type="env.port443Free ? 'success' : 'warning'">
            {{ env.port443Free ? '空闲' : '被 ' + (env.port443Owner || '?') + ' 占用' }}
          </el-tag>
        </div>
        <div class="env-row">
          <span class="env-label">包管理器</span>
          <el-tag>{{ env.packageManager || '未识别' }}</el-tag>
        </div>
      </div>
      <el-alert
        v-if="env && env.issues.length > 0"
        type="warning"
        :closable="false"
        show-icon
        title="检测到阻塞项"
        style="margin-top: 12px"
      >
        <ul style="margin: 0; padding-left: 20px;">
          <li v-for="(i, idx) in env.issues" :key="idx" style="margin-bottom: 4px">{{ i }}</li>
        </ul>
      </el-alert>
    </el-card>

    <!-- 表单 -->
    <el-card>
      <template #header>
        <div style="display: flex; align-items: center; gap: 8px;">
          <el-icon><Lock /></el-icon>
          <span style="font-weight: 600">绑定参数</span>
        </div>
      </template>
      <el-form label-width="160px" label-position="left">
        <el-form-item label="域名">
          <el-input v-model="form.domain" placeholder="如:node1.example.com" />
        </el-form-item>
        <el-form-item label="邮箱">
          <el-input v-model="form.email" placeholder="Let's Encrypt 注册用,接收续费通知" />
        </el-form-item>
        <el-form-item label="证书验证方式">
          <el-radio-group v-model="form.acmeMode">
            <el-radio value="dns01-cf">DNS-01 (CF API)</el-radio>
            <el-radio value="http01">HTTP-01 (临时占用 80 端口)</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="form.acmeMode === 'dns01-cf'" label="CF API Token">
          <el-input
            v-model="form.cfApiToken"
            type="password"
            show-password
            placeholder="Zone:DNS:Edit 权限,在 CF Dashboard → My Profile → API Tokens 创建"
          />
          <span class="nx-muted" style="font-size: 12px">
            绑定成功后会自动加密保存到面板设置(供后续切换橙云/灰云复用)。
            申请证书时只作为环境变量传给 acme.sh 子进程,不写明文 / 不入日志。
          </span>
        </el-form-item>
        <el-alert
          v-else
          type="info"
          :closable="false"
          show-icon
          title="HTTP-01 注意事项"
          description="申请证书那一刻 acme.sh 会临时占用 80 端口跑一次性 HTTP server。
          如果你的域名在 CF 是橙云代理,需要先到 CF Dashboard 把该域名 DNS 临时改为灰云
          (DNS-only),acme.sh 才能从 Let's Encrypt 那边收到来访请求。证书拿到后再改回橙云。
          后续每 60 天续费要重复这个动作。如果你不想折腾,推荐用 DNS-01。"
          style="margin-bottom: 12px"
        />
        <el-form-item label="自动安装缺失依赖">
          <el-switch v-model="form.installMissing" />
          <span class="nx-muted" style="font-size: 12px; margin-left: 8px">
            缺 nginx / acme.sh 时自动 apt-get install / curl 安装
          </span>
        </el-form-item>
        <el-form-item>
          <el-button
            type="primary"
            :loading="binding"
            :disabled="!env || !env.panelIsRoot"
            @click="doBind"
          >开始绑定</el-button>
          <span v-if="binding" class="nx-muted" style="font-size: 12px; margin-left: 8px">
            申请证书 ~30s,nginx + xray 重启 ~10s,总计约 1-3 分钟,请耐心等
          </span>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- Cloudflare 代理状态 -->
    <el-card style="margin-top: 16px">
      <template #header>
        <div style="display: flex; align-items: center; gap: 8px;">
          <span style="font-weight: 600">Cloudflare 代理状态</span>
          <span class="nx-muted" style="font-size: 12px">— 一键切换橙云 / 灰云</span>
        </div>
      </template>
      <p class="nx-muted" style="margin: 0 0 12px 0; font-size: 13px;">
        需要先在「面板设置 → 基础」里填好 CF API token。token 权限要求
        <code>Zone:DNS:Edit</code>。橙云 = 走 CF 代理(隐藏 origin IP);
        灰云 = DNS-only 直连 origin(取 HTTP-01 证书时需要短暂切灰云)。
      </p>
      <el-form label-width="120px" label-position="left" inline>
        <el-form-item label="域名">
          <el-input
            v-model="cfDomain"
            placeholder="如:node1.example.com"
            style="width: 280px"
            @keyup.enter="cfRefresh"
          />
        </el-form-item>
        <el-form-item>
          <el-button :loading="cfLoading" @click="cfRefresh">查询</el-button>
        </el-form-item>
      </el-form>
      <el-alert
        v-if="cfError"
        type="error"
        :closable="false"
        show-icon
        :title="cfError"
        style="margin-top: 8px"
      />
      <div v-if="cfState" class="cf-state-row">
        <div class="cf-state-info">
          <div>Zone: <code>{{ cfState.zoneName }}</code></div>
          <div>记录: <code>{{ cfState.type }}</code> → <code>{{ cfState.content }}</code></div>
          <div>当前状态:
            <el-tag :type="cfState.proxied ? 'warning' : 'info'" size="small">
              {{ cfState.proxied ? '橙云(代理开启)' : '灰云(DNS-only)' }}
            </el-tag>
          </div>
        </div>
        <div class="cf-state-actions">
          <el-button
            type="warning"
            :disabled="cfState.proxied || cfToggling"
            :loading="cfToggling && !cfState.proxied"
            @click="cfToggle(true)"
          >切到橙云</el-button>
          <el-button
            type="info"
            plain
            :disabled="!cfState.proxied || cfToggling"
            :loading="cfToggling && cfState.proxied"
            @click="cfToggle(false)"
          >切到灰云</el-button>
        </div>
      </div>
    </el-card>

    <!-- 结果 -->
    <el-card v-if="result" style="margin-top: 16px">
      <template #header>
        <div style="display: flex; align-items: center; justify-content: space-between; gap: 8px;">
          <div style="display: flex; align-items: center; gap: 8px;">
            <el-tag :type="result.success ? 'success' : 'danger'">
              {{ result.success ? '绑定成功' : '绑定失败' }}
            </el-tag>
            <span v-if="result.domain" class="nx-mono">{{ result.domain }}</span>
          </div>
          <el-button
            v-if="result.success && result.domain"
            type="danger"
            plain
            size="small"
            :loading="unbinding"
            @click="doUnbind"
          >解绑</el-button>
        </div>
      </template>
      <div v-if="result.success">
        <p class="nx-muted">
          域名:<code>{{ result.domain }}</code><br>
          nginx 配置:<code>{{ result.nginxConfPath }}</code><br>
          xray 入站 #{{ result.inboundId }}:<code>127.0.0.1:{{ result.xrayPort }} ws path={{ result.wsPath }}</code><br>
          客户端配置:vless://&lt;UUID&gt;@{{ result.domain }}:443?encryption=none&amp;security=tls&amp;type=ws&amp;host={{ result.domain }}&amp;path={{ encodeURIComponent(result.wsPath || '') }}&amp;sni={{ result.domain }}
        </p>
        <p class="nx-muted" style="font-size: 12px;">
          完整可分享链接(含 UUID + 用户备注前缀)请去入站列表,点新建的 #{{ result.inboundId }} 行的「客户端」→「二维码」拿。
        </p>
      </div>
      <el-alert v-else type="error" :closable="false" show-icon :title="result.error" style="margin-bottom: 12px" />
      <details>
        <summary style="cursor: pointer; user-select: none; margin-bottom: 6px;">详细日志</summary>
        <pre class="log-pane">{{ result.logs.join('\n') }}</pre>
      </details>
    </el-card>
  </div>
</template>

<style scoped>
.env-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 8px 16px;
}
.env-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.env-label {
  width: 130px;
  flex-shrink: 0;
  color: var(--nx-text-muted);
  font-size: 13px;
}
.log-pane {
  background: var(--nx-bg);
  padding: 10px 12px;
  border-radius: 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  max-height: 360px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
.cf-state-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  background: var(--nx-bg);
  padding: 12px 14px;
  border-radius: 6px;
  margin-top: 8px;
}
.cf-state-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 13px;
}
.cf-state-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}
</style>
