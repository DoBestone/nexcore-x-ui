import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { basePath } from '@/utils/base'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/Login.vue'),
    meta: { public: true }
  },
  {
    // 快捷登录链接落地页 — 后端把 token 放在 URL 片段(#tk=...),
    // 这条公共路由只负责加载 SPA 壳,组件读取 location.hash 后调
    // POST /xui/api/panel-login/consume 换 cookie,再跳 /dashboard。
    path: '/magic-login',
    name: 'magic-login',
    component: () => import('@/views/MagicLogin.vue'),
    meta: { public: true }
  },
  {
    path: '/',
    component: () => import('@/views/Layout.vue'),
    redirect: '/dashboard',
    children: [
      {
        path: 'dashboard',
        name: 'dashboard',
        component: () => import('@/views/Dashboard.vue'),
        meta: { title: '系统状态', icon: 'Odometer' }
      },
      {
        path: 'inbounds',
        name: 'inbounds',
        component: () => import('@/views/Inbounds.vue'),
        meta: { title: '入站列表', icon: 'Connection' }
      },
      {
        path: 'outbounds',
        name: 'outbounds',
        component: () => import('@/views/Outbounds.vue'),
        meta: { title: '出站配置', icon: 'Promotion' }
      },
      {
        path: 'block-rules',
        name: 'block-rules',
        component: () => import('@/views/BlockRules.vue'),
        meta: { title: '屏蔽规则', icon: 'Lock' }
      },
      {
        path: 'api-console',
        name: 'api-console',
        component: () => import('@/views/ApiConsole.vue'),
        meta: { title: 'API 控制台', icon: 'Document' }
      },
      {
        path: 'settings',
        name: 'settings',
        component: () => import('@/views/Settings.vue'),
        meta: { title: '面板设置', icon: 'Setting' }
      }
    ]
  },
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' }
]

const router = createRouter({
  // basePath comes from window.__NX_BASE__ injected by the Go server, so
  // the SPA works under any operator-configured base ("/" by default,
  // "/admin/" if mounted behind a path prefix).
  history: createWebHistory(basePath),
  routes
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (to.meta.public) return true
  if (!auth.checked) {
    await auth.refresh()
  }
  if (!auth.isLogin) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
