import { defineStore } from 'pinia'
import { api, type Task, type TaskDetail } from '../api/client'

export const useTasksStore = defineStore('tasks', {
  state: () => ({
    tasks: [] as Task[],
    detail: null as TaskDetail | null,
    loading: false,
    error: ''
  }),
  actions: {
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
