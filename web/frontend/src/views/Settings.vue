<script setup lang="ts">
import { onMounted, ref } from 'vue'
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
              <el-form-item>
                <el-button type="primary" :loading="saving" @click="save">保存</el-button>
                <el-button type="danger" plain @click="restart">重启面板</el-button>
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
