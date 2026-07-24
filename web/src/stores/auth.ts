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
  /** 初始化管理员令牌和 Manager 状态。 */
  state: () => ({
    token: localStorage.getItem('wanxiang_admin_token') || '',
    manager: null as ManagerStatus | null
  }),
  actions: {
    /** 将管理员令牌同步到 Store 与本地存储。 */
    saveToken(token: string) {
      this.token = token
      localStorage.setItem('wanxiang_admin_token', token)
    },
    /** 校验管理员账号并保存登录令牌。 */
    async login(username: string, password: string) {
      const res = await api<LoginResponse>('/api/admin/login', {
        method: 'POST',
        body: JSON.stringify({ username, password })
      })
      this.saveToken(res.token)
    },
    /** 初始化首个管理员账号并保存登录令牌。 */
    async bootstrap(username: string, password: string) {
      const res = await api<LoginResponse>('/api/admin/bootstrap', {
        method: 'POST',
        body: JSON.stringify({ username, password })
      })
      this.saveToken(res.token)
    },
    /** 加载 Manager 状态及缺失配置。 */
    async loadManager() {
      const res = await api<ManagerResponse>('/api/admin/manager/status')
      this.manager = res.manager
      return res.manager
    },
    /** 保存 Manager 密钥并刷新状态。 */
    async saveManagerSecret(key: string, value: string) {
      await api<{ ok: boolean }>('/api/admin/manager/secrets', {
        method: 'POST',
        body: JSON.stringify({ key, value })
      })
      return this.loadManager()
    }
  }
})
