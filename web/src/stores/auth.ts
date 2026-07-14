import { defineStore } from 'pinia'
import { api, type ManagerStatus } from '../api/client'

interface LoginResponse {
  ok: boolean
  token: string
}

interface ManagerResponse {
  ok: boolean
  manager: ManagerStatus
}

export const useAuthStore = defineStore('auth', {
  state: () => ({
    token: localStorage.getItem('wanxiang_admin_token') || '',
    manager: null as ManagerStatus | null
  }),
  actions: {
    saveToken(token: string) {
      this.token = token
      localStorage.setItem('wanxiang_admin_token', token)
    },
    async login(username: string, password: string) {
      const res = await api<LoginResponse>('/api/admin/login', {
        method: 'POST',
        body: JSON.stringify({ username, password })
      })
      this.saveToken(res.token)
    },
    async bootstrap(username: string, password: string) {
      const res = await api<LoginResponse>('/api/admin/bootstrap', {
        method: 'POST',
        body: JSON.stringify({ username, password })
      })
      this.saveToken(res.token)
    },
    async loadManager() {
      const res = await api<ManagerResponse>('/api/admin/manager/status')
      this.manager = res.manager
      return res.manager
    },
    async saveManagerSecret(key: string, value: string) {
      await api<{ ok: boolean }>('/api/admin/manager/secrets', {
        method: 'POST',
        body: JSON.stringify({ key, value })
      })
      return this.loadManager()
    }
  }
})
