<script setup lang="ts">
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { User, Lock, Cpu, Lightning, DataLine } from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()

const form = ref({ username: '', password: '' })
const loading = ref(false)
const year = new Date().getFullYear()

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
    <!-- 左侧:品牌/氛围面板(装饰位允许放开) -->
    <aside class="login-aside">
      <div class="aside-grid" aria-hidden="true"></div>
      <div class="aside-glow aside-glow--a" aria-hidden="true"></div>
      <div class="aside-glow aside-glow--b" aria-hidden="true"></div>

      <div class="aside-inner">
        <div class="brand-row">
          <div class="brand-mark">N</div>
          <div class="brand-text">
            <div class="brand-name">NexCore X-UI</div>
            <div class="brand-sub">Xray 面板 · 控制台</div>
          </div>
        </div>

        <h1 class="aside-title">
          轻量、稳定的<br />
          <span class="accent">Xray 节点控制台</span>
        </h1>
        <p class="aside-desc">
          为 1H1G 小机型量身设计的入站管理、流量统计与多用户分发面板。
        </p>

        <ul class="aside-feats">
          <li>
            <el-icon class="feat-ico"><Lightning /></el-icon>
            <div>
              <div class="feat-t">极致轻量</div>
              <div class="feat-d">单二进制部署,常驻内存 &lt; 60 MB</div>
            </div>
          </li>
          <li>
            <el-icon class="feat-ico"><DataLine /></el-icon>
            <div>
              <div class="feat-t">流量可视化</div>
              <div class="feat-d">入站 / 客户端双维度统计与配额</div>
            </div>
          </li>
          <li>
            <el-icon class="feat-ico"><Cpu /></el-icon>
            <div>
              <div class="feat-t">多协议内核</div>
              <div class="feat-d">VLESS / VMess / Trojan / Shadowsocks</div>
            </div>
          </li>
        </ul>

        <div class="aside-footer">© {{ year }} NexCore · 控制台</div>
      </div>
    </aside>

    <!-- 右侧:登录表单 -->
    <main class="login-main">
      <!-- 移动端品牌头(仅小屏可见) -->
      <div class="brand-row brand-row--mobile">
        <div class="brand-mark">N</div>
        <div class="brand-text">
          <div class="brand-name">NexCore X-UI</div>
          <div class="brand-sub">面板控制台</div>
        </div>
      </div>

      <div class="login-card">
        <div class="card-head">
          <h2 class="card-title">欢迎回来</h2>
          <p class="card-desc">请使用面板账号登录,继续管理你的节点。</p>
        </div>

        <el-form @submit.prevent="submit" label-position="top" class="login-form">
          <el-form-item label="用户名">
            <el-input
              v-model="form.username"
              size="large"
              autocomplete="username"
              placeholder="admin"
              :prefix-icon="User"
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
              placeholder="请输入密码"
              :prefix-icon="Lock"
              @keyup.enter="submit"
            />
          </el-form-item>
          <el-button
            type="primary"
            size="large"
            :loading="loading"
            class="submit-btn"
            @click="submit"
          >
            登 录
          </el-button>
        </el-form>

        <div class="card-foot">
          <el-icon><Lock /></el-icon>
          <span>会话仅保存在当前浏览器,不会上传任何凭据。</span>
        </div>
      </div>

      <div class="main-footer">© {{ year }} NexCore · X-UI</div>
    </main>
  </div>
</template>

<style scoped>
/* 装饰位字体:不依赖 Manrope,系统栈 + 字距收紧避免 AI 默认感 */
.login-page {
  --font-display: ui-sans-serif, system-ui, -apple-system, 'PingFang SC', 'Microsoft YaHei',
    sans-serif;
  --font-body: ui-sans-serif, system-ui, -apple-system, 'PingFang SC', 'Microsoft YaHei',
    sans-serif;

  min-height: 100vh;
  display: grid;
  grid-template-columns: minmax(0, 1.05fr) minmax(0, 1fr);
  background: var(--nx-bg);
  font-family: var(--font-body);
}

/* ============ 左侧氛围面板 ============ */
.login-aside {
  position: relative;
  overflow: hidden;
  background:
    linear-gradient(160deg, #1d4ed8 0%, #2563eb 45%, #3b82f6 100%);
  color: #fff;
  display: flex;
}

/* 细网格,克制不抢戏 */
.aside-grid {
  position: absolute;
  inset: 0;
  background-image:
    linear-gradient(rgba(255, 255, 255, 0.06) 1px, transparent 1px),
    linear-gradient(90deg, rgba(255, 255, 255, 0.06) 1px, transparent 1px);
  background-size: 28px 28px;
  mask-image: radial-gradient(ellipse at 30% 40%, #000 35%, transparent 75%);
  -webkit-mask-image: radial-gradient(ellipse at 30% 40%, #000 35%, transparent 75%);
}

.aside-glow {
  position: absolute;
  border-radius: 50%;
  filter: blur(60px);
  opacity: 0.55;
  pointer-events: none;
}
.aside-glow--a {
  width: 360px;
  height: 360px;
  top: -80px;
  left: -80px;
  background: #60a5fa;
}
.aside-glow--b {
  width: 480px;
  height: 480px;
  bottom: -160px;
  right: -120px;
  background: #1e40af;
  opacity: 0.6;
}

.aside-inner {
  position: relative;
  z-index: 1;
  margin: auto;
  padding: 56px 64px;
  max-width: 520px;
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 32px;
}

.brand-row {
  display: flex;
  align-items: center;
  gap: 12px;
}
.brand-mark {
  width: 40px;
  height: 40px;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.15);
  border: 1px solid rgba(255, 255, 255, 0.25);
  backdrop-filter: blur(8px);
  color: #fff;
  font-family: var(--font-display);
  font-weight: 700;
  font-size: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  letter-spacing: -0.02em;
}
.brand-name {
  font-family: var(--font-display);
  font-weight: 600;
  font-size: 15px;
  letter-spacing: -0.01em;
}
.brand-sub {
  font-size: 12px;
  color: rgba(255, 255, 255, 0.7);
  margin-top: 2px;
}

.aside-title {
  font-family: var(--font-display);
  font-size: 38px;
  line-height: 1.2;
  font-weight: 700;
  letter-spacing: -0.02em;
  margin: 0;
  color: #fff;
}
.aside-title .accent {
  color: #bfdbfe;
}

.aside-desc {
  font-size: 14px;
  line-height: 1.65;
  color: rgba(255, 255, 255, 0.78);
  margin: -8px 0 0;
  max-width: 420px;
}

.aside-feats {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.aside-feats li {
  display: flex;
  gap: 14px;
  align-items: flex-start;
  opacity: 0;
  transform: translateY(8px);
  animation: rise 0.5s ease-out forwards;
}
.aside-feats li:nth-child(1) { animation-delay: 80ms; }
.aside-feats li:nth-child(2) { animation-delay: 160ms; }
.aside-feats li:nth-child(3) { animation-delay: 240ms; }

.feat-ico {
  flex: 0 0 auto;
  width: 36px;
  height: 36px;
  border-radius: 8px;
  background: rgba(255, 255, 255, 0.12);
  border: 1px solid rgba(255, 255, 255, 0.2);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 18px;
  color: #fff;
}
.feat-t {
  font-family: var(--font-display);
  font-weight: 600;
  font-size: 14px;
  letter-spacing: -0.005em;
  color: #fff;
}
.feat-d {
  font-size: 12.5px;
  color: rgba(255, 255, 255, 0.7);
  margin-top: 2px;
  line-height: 1.5;
}

.aside-footer {
  font-size: 12px;
  color: rgba(255, 255, 255, 0.5);
  margin-top: auto;
}

/* ============ 右侧表单 ============ */
.login-main {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 48px 32px;
  background:
    radial-gradient(circle at 90% 0%, #eff6ff 0%, transparent 45%),
    radial-gradient(circle at 0% 100%, #e0e7ff 0%, transparent 50%),
    var(--nx-bg);
}

.brand-row--mobile {
  display: none;
}

.login-card {
  width: 100%;
  max-width: 400px;
  padding: 36px;
  background: var(--nx-bg-card);
  border: 1px solid var(--nx-border);
  border-radius: 14px;
  box-shadow: 0 12px 40px rgba(15, 23, 42, 0.06);
  opacity: 0;
  transform: translateY(8px);
  animation: rise 0.45s ease-out 0.1s forwards;
}

.card-head {
  margin-bottom: 24px;
}
.card-title {
  font-family: var(--font-display);
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.02em;
  color: var(--nx-text-strong);
  margin: 0 0 6px;
}
.card-desc {
  font-size: 13px;
  color: var(--nx-text-muted);
  margin: 0;
  line-height: 1.55;
}

.login-form :deep(.el-form-item__label) {
  font-size: 13px;
  font-weight: 500;
  color: var(--nx-text);
  padding-bottom: 6px;
}
.login-form :deep(.el-input__wrapper) {
  border-radius: 10px;
  box-shadow: 0 0 0 1px var(--nx-border) inset;
  transition: box-shadow 0.15s;
}
.login-form :deep(.el-input__wrapper:hover) {
  box-shadow: 0 0 0 1px #cbd5e1 inset;
}
.login-form :deep(.el-input__wrapper.is-focus) {
  box-shadow: 0 0 0 1px var(--nx-primary) inset, 0 0 0 3px rgba(59, 130, 246, 0.12);
}

.submit-btn {
  width: 100%;
  height: 46px;
  margin-top: 4px;
  font-size: 15px;
  font-weight: 600;
  letter-spacing: 0.4em;
  text-indent: 0.4em;
  border-radius: 10px;
  background: linear-gradient(180deg, #3b82f6 0%, #2563eb 100%);
  border: none;
  box-shadow: 0 1px 0 rgba(255, 255, 255, 0.15) inset, 0 4px 14px rgba(37, 99, 235, 0.28);
  transition: transform 0.12s ease, box-shadow 0.15s ease;
}
.submit-btn:hover {
  box-shadow: 0 1px 0 rgba(255, 255, 255, 0.15) inset, 0 6px 18px rgba(37, 99, 235, 0.36);
}
.submit-btn:active {
  transform: translateY(1px);
}

.card-foot {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px dashed var(--nx-border);
  font-size: 12px;
  color: var(--nx-text-muted);
  line-height: 1.5;
}
.card-foot .el-icon {
  font-size: 13px;
  color: var(--nx-text-soft);
}

.main-footer {
  margin-top: 24px;
  font-size: 12px;
  color: var(--nx-text-soft);
}

@keyframes rise {
  to {
    opacity: 1;
    transform: none;
  }
}

/* ============ 响应式 ============ */
@media (max-width: 992px) {
  .aside-inner {
    padding: 40px 40px;
  }
  .aside-title {
    font-size: 32px;
  }
}

@media (max-width: 768px) {
  .login-page {
    grid-template-columns: 1fr;
  }
  .login-aside {
    display: none;
  }
  .login-main {
    padding: 24px 16px;
    min-height: 100vh;
  }
  .brand-row--mobile {
    display: flex;
    margin-bottom: 24px;
    color: var(--nx-text-strong);
  }
  .brand-row--mobile .brand-mark {
    background: linear-gradient(135deg, #2563eb, #4f46e5);
    border: none;
    color: #fff;
  }
  .brand-row--mobile .brand-name {
    color: var(--nx-text-strong);
  }
  .brand-row--mobile .brand-sub {
    color: var(--nx-text-muted);
  }
  .login-card {
    padding: 28px 24px;
    border-radius: 12px;
  }
  .card-title {
    font-size: 20px;
  }
}

@media (max-width: 380px) {
  .login-card {
    padding: 24px 18px;
  }
}
</style>
