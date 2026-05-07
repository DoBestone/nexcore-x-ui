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
      try {
        await http.get('logout')
      } catch {
        /* ignore — clear local anyway */
      }
      this.me = null
      this.checked = true
    }
  }
})
