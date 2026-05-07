<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { postForm } from '@/api/http'
import type { AllSetting } from '@/api/types'

const all = ref<AllSetting | null>(null)
const loading = ref(false)
const saving = ref(false)

async function load() {
  loading.value = true
  try {
    all.value = await postForm<AllSetting>('xui/setting/all')
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!all.value) return
  saving.value = true
  try {
    await postForm('xui/setting/update', all.value as unknown as Record<string, unknown>)
    ElMessage.success('已保存')
  } finally {
    saving.value = false
  }
}

// ---------- 改密 ----------
const userForm = ref({
  oldUsername: '',
  oldPassword: '',
  newUsername: '',
  newPassword: ''
})

async function updateUser() {
  if (!userForm.value.newUsername || !userForm.value.newPassword) {
    ElMessage.warning('新用户名和新密码不能为空')
    return
  }
  await postForm('xui/setting/updateUser', userForm.value as unknown as Record<string, unknown>)
  ElMessage.success('已修改,所有 token 已失效,请重新登录')
}

// ---------- 重启面板 ----------
async function restart() {
  try {
    await ElMessageBox.confirm('确认重启面板?约 3 秒后断开连接,刷新页面继续。', '重启面板', {
      type: 'warning',
      confirmButtonText: '重启',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  await postForm('xui/setting/restartPanel')
  ElMessage.success('已发起重启')
}

// ---------- 在线 IP webhook 测试 ----------
const testingWebhook = ref(false)
async function testWebhook() {
  if (!all.value?.onlineWebhookUrl) {
    ElMessage.warning('请先填写并保存 Webhook URL')
    return
  }
  testingWebhook.value = true
  try {
    await postForm('xui/setting/testOnlineWebhook')
    ElMessage.success('已推送一次,请检查业务系统是否收到')
  } catch (e: unknown) {
    const msg = (e as { response?: { data?: { msg?: string } } })?.response?.data?.msg || '推送失败'
    ElMessage.error(msg)
  } finally {
    testingWebhook.value = false
  }
}

// ---------- 安全入口 ----------
// 启用后必须以 webBasePath + secureEntryPath/ 访问面板,扫端口看到
// 裸 404 不再吐 SPA。生成按钮:24 字符随机串(letters+digits),足够
// 让端口扫描+暴力组合不现实。开启前给完整新 URL,提示用户记下来 +
// 重启面板才生效 — 因为 basePath 在 initRouter 一次性读取。
function randomSlug(len = 24): string {
  const chars = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'
  const arr = new Uint8Array(len)
  if (typeof crypto !== 'undefined') {
    crypto.getRandomValues(arr)
  } else {
    for (let i = 0; i < len; i++) arr[i] = Math.floor(Math.random() * 256)
  }
  let out = ''
  for (let i = 0; i < len; i++) out += chars[arr[i] % chars.length]
  return out
}

function regenSecureEntry() {
  if (!all.value) return
  all.value.secureEntryPath = randomSlug(24)
}

// 当前预览:把 webBasePath + secureEntryPath/ 拼出来给用户看,启用前
// 一眼就能知道新 URL 长什么样;同时计算完整 url(含 origin)方便复制。
const previewBase = computed(() => {
  const s = all.value
  if (!s) return ''
  const base = s.webBasePath || '/'
  const slug = (s.secureEntryPath || '').trim()
  if (!slug) return base
  return (base.endsWith('/') ? base : base + '/') + slug + '/'
})

const previewUrl = computed(() => {
  if (typeof window === 'undefined') return previewBase.value
  return window.location.origin + previewBase.value
})

async function saveSecureEntry() {
  if (!all.value) return
  // 启用前先校验 path 非空 — 后端 CheckValid 也会拒,这里前置一次
  // 给更友好的弹窗。
  if (all.value.secureEntryEnabled && !(all.value.secureEntryPath || '').trim()) {
    ElMessage.warning('启用前请先生成或填写安全入口路径')
    return
  }
  const action = all.value.secureEntryEnabled ? '启用' : '关闭'
  try {
    await ElMessageBox.confirm(
      all.value.secureEntryEnabled
        ? `确认启用安全入口?保存并重启面板后,只能通过下面的 URL 进入,务必先记下:\n\n${previewUrl.value}\n\n忘了 URL 可以 SSH 到服务器查 SQLite settings 表恢复。`
        : '确认关闭安全入口?面板将退回到普通 URL 直接可达。',
      `${action}安全入口`,
      {
        type: 'warning',
        confirmButtonText: action,
        cancelButtonText: '取消',
        dangerouslyUseHTMLString: false
      }
    )
  } catch {
    return
  }
  await postForm('xui/setting/update', all.value as unknown as Record<string, unknown>)
  ElMessage.success(`已${action},点「重启面板」生效;新 URL: ${previewUrl.value}`)
}

async function copyEntryUrl() {
  const text = previewUrl.value
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(text)
      ElMessage.success('已复制')
      return
    }
  } catch {
    /* fallthrough */
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  const ok = document.execCommand('copy')
  document.body.removeChild(ta)
  ElMessage[ok ? 'success' : 'warning'](ok ? '已复制' : '复制失败')
}

// ---------- magic link ----------
const magicTtl = ref(600)
const magicLink = ref('')
async function createMagic() {
  const r = await postForm<{ url: string; token: string; expiresAt: number }>(
    'xui/setting/magicLink',
    { ttlSeconds: magicTtl.value }
  )
  magicLink.value = r.url
}

async function copyMagic() {
  if (!magicLink.value) return
  try {
    await navigator.clipboard.writeText(magicLink.value)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败,请手动选择文本')
  }
}

onMounted(() => {
  load()
})
</script>

<template>
  <div class="nx-page">
    <h2>面板设置</h2>

    <el-tabs>
      <el-tab-pane label="基础" lazy>
        <el-card>
          <div v-if="all">
            <el-form label-width="160px" label-position="left">
              <el-form-item label="监听 IP">
                <el-input v-model="all.webListen" placeholder="留空 = 0.0.0.0" />
              </el-form-item>
              <el-form-item label="端口">
                <el-input-number v-model="all.webPort" :min="1" :max="65535" />
              </el-form-item>
              <el-form-item label="基础路径">
                <el-input v-model="all.webBasePath" placeholder="/ (默认)" />
              </el-form-item>
              <el-form-item label="证书文件">
                <el-input v-model="all.webCertFile" placeholder="留空 = HTTP" />
              </el-form-item>
              <el-form-item label="证书私钥">
                <el-input v-model="all.webKeyFile" />
              </el-form-item>
              <el-form-item label="时区">
                <el-input v-model="all.timeLocation" />
              </el-form-item>
              <el-form-item label="节点名称">
                <el-input
                  v-model="all.nodeName"
                  placeholder="如:香港节点1 (留空 = 分享链接不加前缀)"
                />
                <span class="nx-muted" style="font-size: 12px">
                  会以 <code>[节点名称] email</code> 注入到 vmess/vless/trojan/ss
                  分享链接的 ps 字段;入站绑了出站时改用该出站的名称作为前缀。
                </span>
              </el-form-item>
              <el-form-item label="节点地址">
                <el-input
                  v-model="all.nodeAddress"
                  placeholder="如:1.2.3.4 或 node1.example.com (留空 = 跟面板访问域名)"
                />
                <span class="nx-muted" style="font-size: 12px">
                  分享链接里写的 host。Cloudflare 橙云代理 + 域名访问面板时
                  <strong>必须显式配</strong> —— 否则链接 host 是 CF 代理域,
                  客户端打 xray 的非标端口被 CF 丢弃造成连接超时。一般填
                  origin 直连 IP,或者另一个 DNS-only 子域(灰云)指向 origin。
                </span>
              </el-form-item>
              <el-form-item label="CF API Token">
                <el-input
                  v-model="all.cfApiToken"
                  type="password"
                  show-password
                  placeholder="留空 = 不修改(已存的不会被清掉)"
                />
                <span class="nx-muted" style="font-size: 12px">
                  Zone:DNS:Edit 权限,在
                  <a href="https://dash.cloudflare.com/profile/api-tokens" target="_blank">CF Profile → API Tokens</a>
                  生成。用于域名绑定 DNS-01 取证书 + 一键切橙云灰云。
                  服务侧 AES-GCM 加密存储,前端不回读明文,你看到空就是没改。
                </span>
              </el-form-item>
              <el-form-item>
                <el-button type="primary" :loading="saving" @click="save">保存</el-button>
                <el-button type="danger" plain @click="restart">重启面板</el-button>
              </el-form-item>
            </el-form>
          </div>
        </el-card>

        <!-- 安全入口卡片:把整个面板锁到一个秘密 URL 后面。启用前展示
             完整新 URL + 大字号警告,因为忘了入口=自己进不来。 -->
        <el-card style="margin-top: 16px">
          <template #header>
            <div style="display: flex; align-items: center; gap: 8px;">
              <span style="font-weight: 600">安全入口</span>
              <el-tag v-if="all?.secureEntryEnabled" type="success" size="small">已启用</el-tag>
              <el-tag v-else type="info" size="small">未启用</el-tag>
            </div>
          </template>
          <p class="nx-muted" style="margin: 0 0 12px 0; font-size: 13px;">
            启用后,面板只在下方「完整 URL」下应答,任何其它路径返回 404。
            扫端口的工具看不到登录界面,显著降低被自动化工具发现的概率。
            <strong>启用前务必先生成路径并记下完整 URL,否则保存重启后自己也进不来。</strong>
          </p>
          <div v-if="all">
            <el-form label-width="120px" label-position="left">
              <el-form-item label="启用安全入口">
                <el-switch v-model="all.secureEntryEnabled" />
              </el-form-item>
              <el-form-item label="入口路径">
                <el-input
                  v-model="all.secureEntryPath"
                  placeholder="留空 = 未配置;字母/数字/_-,4-64 位"
                >
                  <template #append>
                    <el-button @click="regenSecureEntry">随机生成</el-button>
                  </template>
                </el-input>
              </el-form-item>
              <el-form-item label="完整 URL">
                <div style="display: flex; align-items: center; gap: 8px; flex-wrap: wrap; flex: 1; min-width: 0;">
                  <code style="background: var(--nx-bg); padding: 4px 10px; border-radius: 4px; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12.5px; word-break: break-all; flex: 1; min-width: 0;">{{ previewUrl }}</code>
                  <el-button size="small" type="primary" link @click="copyEntryUrl">复制</el-button>
                </div>
              </el-form-item>
              <el-form-item>
                <el-button type="primary" @click="saveSecureEntry">保存安全入口设置</el-button>
                <span class="nx-muted" style="font-size: 12px; margin-left: 8px">
                  保存后点「重启面板」即生效
                </span>
              </el-form-item>
            </el-form>
          </div>
        </el-card>
      </el-tab-pane>

      <el-tab-pane label="在线 IP Webhook" lazy>
        <el-card>
          <p class="nx-muted">
            把本节点的"email → 在线 IP"快照定时推给上游业务系统,用于多节点共享
            "max devices" 配额聚合。每 5 秒比对一次状态,有变化才发出。
            URL 留空 = 禁用。
          </p>
          <p class="nx-muted">
            POST 请求体:<code>{ node_id, ts, online: { email: [ip,...] } }</code>。
            配置了 secret 时附 <code>X-Nx-Signature: hex(HMAC-SHA256(secret, body))</code>。
            语义为"幂等替换" — 业务系统应把当前 node_id 名下状态完全覆盖为 online 字段内容。
          </p>
          <div v-if="all">
            <el-form label-width="160px" label-position="left">
              <el-form-item label="Webhook URL">
                <el-input
                  v-model="all.onlineWebhookUrl"
                  placeholder="https://biz.example.com/api/x-ui/online"
                />
              </el-form-item>
              <el-form-item label="HMAC Secret">
                <el-input v-model="all.onlineWebhookSecret" type="password" show-password />
              </el-form-item>
              <el-form-item label="Node ID">
                <el-input
                  v-model="all.onlineWebhookNodeId"
                  placeholder="留空 = 用 hostname"
                />
              </el-form-item>
              <el-form-item>
                <el-button type="primary" :loading="saving" @click="save">保存</el-button>
                <el-button :loading="testingWebhook" @click="testWebhook">测试推送</el-button>
              </el-form-item>
            </el-form>
          </div>
        </el-card>
      </el-tab-pane>

      <el-tab-pane label="Telegram" lazy>
        <el-card>
          <div v-if="all">
            <el-form label-width="160px" label-position="left">
              <el-form-item label="启用 Bot 通知">
                <el-switch v-model="all.tgBotEnable" />
              </el-form-item>
              <el-form-item label="Bot Token">
                <el-input v-model="all.tgBotToken" type="password" show-password />
              </el-form-item>
              <el-form-item label="Chat ID">
                <el-input-number v-model="all.tgBotChatId" />
              </el-form-item>
              <el-form-item label="定时任务">
                <el-input v-model="all.tgRunTime" placeholder="cron 表达式" />
              </el-form-item>
              <el-form-item>
                <el-button type="primary" :loading="saving" @click="save">保存</el-button>
              </el-form-item>
            </el-form>
          </div>
        </el-card>
      </el-tab-pane>

      <el-tab-pane label="Xray 模板" lazy>
        <el-card>
          <div v-if="all">
            <p class="nx-muted">完整 xray 模板配置(JSON),保存后立即触发 xray 重启</p>
            <el-input
              v-model="all.xrayTemplateConfig"
              type="textarea"
              :rows="22"
              class="nx-mono"
            />
            <el-button type="primary" :loading="saving" @click="save" style="margin-top: 12px">
              保存模板
            </el-button>
          </div>
        </el-card>
      </el-tab-pane>

      <el-tab-pane label="账户" lazy>
        <el-card>
          <el-form label-width="160px" label-position="left" style="max-width: 480px">
            <el-form-item label="原用户名">
              <el-input v-model="userForm.oldUsername" />
            </el-form-item>
            <el-form-item label="原密码">
              <el-input v-model="userForm.oldPassword" type="password" show-password />
            </el-form-item>
            <el-form-item label="新用户名">
              <el-input v-model="userForm.newUsername" />
            </el-form-item>
            <el-form-item label="新密码">
              <el-input v-model="userForm.newPassword" type="password" show-password />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" @click="updateUser">修改</el-button>
            </el-form-item>
          </el-form>
        </el-card>
      </el-tab-pane>

      <el-tab-pane label="快捷登录链接" lazy>
        <el-card>
          <p class="nx-muted">
            生成一次性 magic link,用户点击即免密登录(适合临时分享访问权限)。
          </p>
          <el-form label-width="160px" label-position="left" style="max-width: 480px">
            <el-form-item label="有效时长 (秒)">
              <el-input-number v-model="magicTtl" :min="60" :max="86400 * 7" />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" @click="createMagic">生成链接</el-button>
            </el-form-item>
            <el-form-item v-if="magicLink" label="链接">
              <el-input :model-value="magicLink" readonly>
                <template #append>
                  <el-button @click="copyMagic">复制</el-button>
                </template>
              </el-input>
            </el-form-item>
          </el-form>
        </el-card>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>
