<script setup lang="ts">
import { onMounted, ref, type Component } from 'vue'
import { useRoute, useRouter, RouterView } from 'vue-router'
import { ElMessageBox } from 'element-plus'
import {
  Odometer,
  Connection,
  Promotion,
  Lock,
  Link,
  Document,
  Setting,
  User,
  SwitchButton,
  RefreshRight
} from '@element-plus/icons-vue'
import type { ServerStatus } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { post } from '@/api/http'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()

interface NavItem {
  path: string
  title: string
  // Component reference, not string — under按需引入 the resolver does
  // NOT register icons globally, so `<component :is="'Odometer'" />`
  // wouldn't resolve. Bind a real Component (imported above) and the
  // template `<component :is="n.icon" />` works regardless of bundle mode.
  icon: Component
}

// Panel version surfaced in the side-bar. xray.version is the xray-core
// build, NOT the panel; panelVersion is 我们在 server.Status 加的字段。
// /server/status 是 POST(legacy from vaxilu/x-ui),**不在** /xui/ group 下
// —— ServerController 是顶层挂的(web.go),跟 Dashboard.vue 用的路径一致。
// post() 自动 unwrap {success, msg, obj}。
const panelVersion = ref('')
onMounted(async () => {
  try {
    const s = await post<ServerStatus & { panelVersion?: string }>('server/status')
    if (s?.panelVersion) panelVersion.value = s.panelVersion
  } catch {
    /* server may be down or 401-redirecting; fall back to empty string */
  }
})

const nav: NavItem[] = [
  { path: '/dashboard', title: '系统状态', icon: Odometer },
  { path: '/inbounds', title: '入站列表', icon: Connection },
  { path: '/outbounds', title: '出站配置', icon: Promotion },
  { path: '/domains', title: '域名绑定', icon: Link },
  { path: '/block-rules', title: '屏蔽规则', icon: Lock },
  { path: '/api-console', title: 'API 控制台', icon: Document },
  { path: '/system-update', title: '系统更新', icon: RefreshRight },
  { path: '/settings', title: '面板设置', icon: Setting }
]

// 不用 el-menu — 它的 default-active prop 在 router 模式下经常落后
// 一档(内部 active 状态被 click 和 prop watcher 抢着写,batched 更新
// 后留在旧值)。自己写 <nav> + 高亮按 route.path === n.path 直接判断,
// 100% 反应式且简单。
function onSelect(path: string) {
  if (path !== route.path) router.push(path)
}

async function doLogout() {
  try {
    await ElMessageBox.confirm('确认退出登录?', '退出', {
      type: 'warning',
      confirmButtonText: '退出',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  await auth.logout()
  router.replace('/login')
}
</script>

<template>
  <el-container class="layout">
    <el-aside class="side" width="220px">
      <div class="brand">
        <div class="brand-mark">N</div>
        <div class="brand-text">
          <div class="brand-name">NexCore X-UI</div>
          <div class="brand-sub">{{ panelVersion ? 'v' + panelVersion : '面板控制台' }}</div>
        </div>
      </div>
      <nav class="menu">
        <a
          v-for="n in nav"
          :key="n.path"
          class="menu-item"
          :class="{ active: route.path === n.path }"
          @click.prevent="onSelect(n.path)"
          :href="n.path"
        >
          <el-icon><component :is="n.icon" /></el-icon>
          <span>{{ n.title }}</span>
        </a>
      </nav>
      <!-- 侧栏赞助卡:项目方同品牌 9188.pro 综合数字基础服务平台。
           不打 banner 喊话,保持冷调小卡,链接外开。-->
      <a
        href="https://9188.pro/?utm_source=nexcore-panel&utm_medium=sidebar"
        target="_blank"
        rel="noopener noreferrer"
        class="side-sponsor"
      >
        <div class="side-sponsor-title">NexCore · 9188.pro</div>
        <div class="side-sponsor-sub">VPS · 域名 · 主机托管</div>
      </a>
      <div class="side-foot">
        <div class="user">
          <el-icon><User /></el-icon>
          <span>{{ auth.me?.username || '...' }}</span>
        </div>
        <el-button text size="small" @click="doLogout">
          <el-icon><SwitchButton /></el-icon>
          退出
        </el-button>
      </div>
    </el-aside>
    <el-main class="main">
      <RouterView />
    </el-main>
  </el-container>
</template>

<style scoped>
.layout {
  height: 100vh;
}

.side {
  background: #ffffff;
  border-right: 1px solid var(--nx-border);
  display: flex;
  flex-direction: column;
  padding: 16px 0;
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 16px 16px;
  border-bottom: 1px solid var(--nx-border);
  margin-bottom: 8px;
}

.brand-mark {
  width: 36px;
  height: 36px;
  border-radius: 8px;
  background: linear-gradient(135deg, #2563eb, #4f46e5);
  color: #fff;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
}

.brand-name {
  font-weight: 600;
  color: var(--nx-text-strong);
  font-size: 14px;
}

.brand-sub {
  font-size: 11px;
  color: var(--nx-text-soft);
}

.menu {
  flex: 1;
  display: flex;
  flex-direction: column;
  padding: 4px 8px;
}

.menu-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 12px;
  height: 40px;
  border-radius: 8px;
  color: var(--nx-text);
  text-decoration: none;
  font-size: 14px;
  cursor: pointer;
  transition: background 0.15s;
}

.menu-item:hover {
  background: var(--nx-bg);
}

.menu-item.active {
  background: var(--nx-primary-soft);
  color: var(--nx-primary);
  font-weight: 500;
}

.side-sponsor {
  display: block;
  margin: 8px 12px 0;
  padding: 10px 12px;
  border-radius: 8px;
  background: linear-gradient(135deg, #f5f8ff 0%, #eef2ff 100%);
  border: 1px solid #dfe7ff;
  text-decoration: none;
  color: var(--nx-text);
  transition: background 0.15s, transform 0.05s;
}
.side-sponsor:hover {
  background: linear-gradient(135deg, #eef2ff 0%, #dbeafe 100%);
}
.side-sponsor:active {
  transform: scale(0.99);
}
.side-sponsor-title {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--nx-primary);
  margin-bottom: 2px;
}
.side-sponsor-sub {
  font-size: 11px;
  color: var(--nx-text-soft);
}

.side-foot {
  padding: 12px 16px;
  margin-top: 8px;
  border-top: 1px solid var(--nx-border);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.user {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--nx-text-muted);
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.main {
  background: var(--nx-bg);
  padding: 0;
  overflow: auto;
}
</style>
