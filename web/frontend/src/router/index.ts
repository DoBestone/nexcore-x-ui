import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/Login.vue'),
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
  history: createWebHistory('/'),
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
