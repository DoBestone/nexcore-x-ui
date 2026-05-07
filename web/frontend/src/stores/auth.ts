import { defineStore } from 'pinia'
import { http, postForm } from '@/api/http'

export interface MeInfo {
  id: number
  username: string
  loginUsername?: string
}

export const useAuthStore = defineStore('auth', {
  state: () => ({
    me: null as MeInfo | null,
    checked: false
  }),
  getters: {
    isLogin: (s) => s.me !== null
  },
  actions: {
    async refresh() {
      try {
        const r = await http.get<{ success: boolean; obj?: MeInfo }>('xui/api/me')
        if (r.data?.success && r.data.obj) {
          this.me = r.data.obj
        } else {
          this.me = null
        }
      } catch {
        this.me = null
      } finally {
        this.checked = true
      }
    },
    async login(username: string, password: string) {
      await postForm('login', { username, password })
      await this.refresh()
    },
    async logout() {
      // POST instead of GET — the panel's logout endpoint is now POST
      // so it can't be triggered cross-origin by an <img src> CSRF.
      // originCSRFMiddleware on the same group also requires Origin/
      // Referer to match Host, which a same-origin SPA fetch supplies
      // for free.
      try {
        await http.post('logout')
      } catch {
        /* ignore — clear local anyway */
      }
      this.me = null
      this.checked = true
    }
  }
})
