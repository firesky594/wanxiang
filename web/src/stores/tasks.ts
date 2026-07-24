import { defineStore } from 'pinia'
import { api, type Task, type TaskDetail } from '../api/client'

export const useTasksStore = defineStore('tasks', {
  /** 初始化任务列表、详情和请求状态。 */
  state: () => ({
    tasks: [] as Task[],
    detail: null as TaskDetail | null,
    loading: false,
    error: ''
  }),
  actions: {
    /** 加载后台任务列表并维护加载与错误状态。 */
    async loadList() {
      this.loading = true
      this.error = ''
      try {
        const response = await api<{ ok: boolean; tasks: Task[] }>('/api/admin/tasks?limit=100&offset=0')
        this.tasks = response.tasks
      } catch (error) {
        this.error = error instanceof Error ? error.message : String(error)
        throw error
      } finally {
        this.loading = false
      }
    },
    /** 加载指定任务详情并维护加载与错误状态。 */
    async loadDetail(id: number) {
      this.loading = true
      this.error = ''
      try {
        const response = await api<{ ok: boolean; detail: TaskDetail }>(`/api/admin/tasks/${id}`)
        this.detail = response.detail
      } catch (error) {
        this.error = error instanceof Error ? error.message : String(error)
        throw error
      } finally {
        this.loading = false
      }
    }
  }
})
