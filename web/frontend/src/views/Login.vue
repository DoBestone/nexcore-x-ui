<script setup lang="ts">
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()

const form = ref({ username: '', password: '' })
const loading = ref(false)

async function submit() {
  if (!form.value.username || !form.value.password) {
    ElMessage.warning('请输入用户名和密码')
    return
  }
  loading.value = true
  try {
    await auth.login(form.value.username, form.value.password)
    const redirect = (route.query.redirect as string) || '/dashboard'
    router.replace(redirect)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="brand">
        <div class="brand-mark">N</div>
        <div class="brand-text">
          <div class="brand-name">NexCore X-UI</div>
          <div class="brand-sub">面板控制台</div>
        </div>
      </div>
      <el-form @submit.prevent="submit" label-position="top">
        <el-form-item label="用户名">
          <el-input
            v-model="form.username"
            size="large"
            autocomplete="username"
            placeholder="admin"
            @keyup.enter="submit"
          />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="form.password"
            type="password"
            size="large"
            autocomplete="current-password"
            show-password
            @keyup.enter="submit"
          />
        </el-form-item>
        <el-button
          type="primary"
          size="large"
          :loading="loading"
          @click="submit"
          style="width: 100%"
        >
          登录
        </el-button>
      </el-form>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background:
    radial-gradient(circle at 20% 10%, #dbeafe 0%, transparent 40%),
    radial-gradient(circle at 80% 90%, #e0e7ff 0%, transparent 40%),
    var(--nx-bg);
}

.login-card {
  width: 380px;
  padding: 32px;
  background: var(--nx-bg-card);
  border-radius: 14px;
  box-shadow: 0 8px 32px rgba(15, 23, 42, 0.08);
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 24px;
}

.brand-mark {
  width: 44px;
  height: 44px;
  border-radius: 10px;
  background: linear-gradient(135deg, #2563eb, #4f46e5);
  color: #fff;
  font-weight: 700;
  font-size: 20px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.brand-name {
  font-weight: 600;
  color: var(--nx-text-strong);
  font-size: 16px;
}

.brand-sub {
  font-size: 12px;
  color: var(--nx-text-muted);
  margin-top: 2px;
}
</style>
