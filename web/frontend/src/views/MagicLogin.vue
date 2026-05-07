<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { http } from '@/api/http'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()
const status = ref<'consuming' | 'failed'>('consuming')
const errMsg = ref('')

function readFragmentToken(): string {
  // location.hash starts with '#'; format is `#tk=<plaintext>` (or
  // `#tk=<plain>&...` if we ever stack fragments — defensive parse).
  const raw = window.location.hash.replace(/^#/, '')
  if (!raw) return ''
  for (const part of raw.split('&')) {
    const eq = part.indexOf('=')
    if (eq <= 0) continue
    if (part.slice(0, eq) === 'tk') {
      return decodeURIComponent(part.slice(eq + 1))
    }
  }
  return ''
}

onMounted(async () => {
  const token = readFragmentToken()

  // Wipe the fragment from history before doing anything else: even
  // though it never reached the server, leaving it in window.location
  // means a reload or copy-URL still carries the (already-consumed)
  // token. replaceState swaps it for a clean /magic-login URL.
  if (window.history.replaceState) {
    window.history.replaceState(null, '', window.location.pathname)
  }

  if (!token) {
    status.value = 'failed'
    errMsg.value = '链接无效:缺少凭据'
    return
  }
  try {
    await http.post('panel-login/consume', { token })
    await auth.refresh()
    ElMessage.success('登录成功')
    router.replace('/dashboard')
  } catch (e: unknown) {
    status.value = 'failed'
    // axios 错误优先取 response.data.msg(handleMagicConsume 返回 401
    // 时的语义文案),否则给一个通用兜底。注意:401 拦截器会跳 /login,
    // 但这里捕获后已经覆盖了状态机,interceptor 的跳转也不会再触发。
    const ax = e as { response?: { data?: { msg?: string } }; message?: string }
    errMsg.value = ax?.response?.data?.msg || ax?.message || '链接已失效或已被使用'
  }
})
</script>

<template>
  <div class="magic-page">
    <div class="magic-card">
      <template v-if="status === 'consuming'">
        <el-icon class="magic-spin" :size="32"><Loading /></el-icon>
        <h2 class="magic-title">正在为你登录</h2>
        <p class="magic-desc">校验快捷登录链接,稍候片刻…</p>
      </template>
      <template v-else>
        <el-icon class="magic-fail" :size="32"><WarningFilled /></el-icon>
        <h2 class="magic-title">登录失败</h2>
        <p class="magic-desc">{{ errMsg }}</p>
        <el-button type="primary" @click="router.replace('/login')">返回登录</el-button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.magic-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background:
    radial-gradient(circle at 90% 0%, #eff6ff 0%, transparent 45%),
    radial-gradient(circle at 0% 100%, #e0e7ff 0%, transparent 50%),
    var(--nx-bg);
  padding: 24px;
}

.magic-card {
  width: 100%;
  max-width: 380px;
  padding: 36px 32px;
  background: var(--nx-bg-card);
  border: 1px solid var(--nx-border);
  border-radius: 14px;
  box-shadow: 0 12px 40px rgba(15, 23, 42, 0.06);
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  gap: 12px;
}

.magic-spin {
  color: var(--nx-primary);
  animation: spin 1s linear infinite;
}
.magic-fail {
  color: var(--nx-danger);
}
.magic-title {
  font-size: 18px;
  font-weight: 600;
  color: var(--nx-text-strong);
  margin: 4px 0 0;
  letter-spacing: -0.01em;
}
.magic-desc {
  font-size: 13px;
  color: var(--nx-text-muted);
  line-height: 1.55;
  margin: 0 0 8px;
  word-break: break-word;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
