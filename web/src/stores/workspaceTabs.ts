import { defineStore } from 'pinia'

export interface WorkspaceTab {
  path: string
  title: string
}

interface PersistedWorkspace {
  tabs?: unknown
  activePath?: unknown
  sidebarCollapsed?: unknown
}

const STORAGE_KEY = 'wanxiang_workspace_v2'

/** 判断未知值是否是结构合法的工作区标签。 */
function isWorkspaceTab(value: unknown): value is WorkspaceTab {
  if (!value || typeof value !== 'object') return false
  const tab = value as Record<string, unknown>
  return typeof tab.path === 'string' && typeof tab.title === 'string'
}

export const useWorkspaceTabsStore = defineStore('workspaceTabs', {
  /** 初始化标签页、激活路径和侧栏折叠状态。 */
  state: () => ({
    tabs: [] as WorkspaceTab[],
    activePath: '',
    sidebarCollapsed: false
  }),
  actions: {
    /** 将当前工作区界面状态持久化到本地存储。 */
    persist() {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify({
        tabs: this.tabs,
        activePath: this.activePath,
        sidebarCollapsed: this.sidebarCollapsed
      }))
    },
    /** 打开并激活指定工作区标签。 */
    openTab(tab: WorkspaceTab) {
      if (!this.tabs.some((item) => item.path === tab.path)) {
        this.tabs.push(tab)
      }
      this.activePath = tab.path
      this.persist()
    },
    /** 激活已经打开的工作区标签。 */
    activateTab(path: string) {
      if (!this.tabs.some((item) => item.path === path)) return
      this.activePath = path
      this.persist()
    },
    /** 关闭标签，并在需要时选择相邻标签。 */
    closeTab(path: string) {
      const index = this.tabs.findIndex((item) => item.path === path)
      if (index < 0) return this.activePath

      const wasActive = this.activePath === path
      this.tabs.splice(index, 1)
      if (wasActive) {
        this.activePath = this.tabs[index]?.path || this.tabs[index - 1]?.path || ''
      }
      this.persist()
      return this.activePath
    },
    /** 更新侧栏折叠状态并立即持久化。 */
    setSidebarCollapsed(value: boolean) {
      this.sidebarCollapsed = value
      this.persist()
    },
    /** 校验允许路径并恢复本地工作区状态。 */
    restore(allowedPaths: Set<string> | ((path: string) => boolean)) {
      const raw = window.localStorage.getItem(STORAGE_KEY)
      if (!raw) return

      try {
        const saved = JSON.parse(raw) as PersistedWorkspace
        const isAllowed = typeof allowedPaths === 'function'
          ? allowedPaths
          : (path: string) => allowedPaths.has(path)
        const restoredTabs = Array.isArray(saved.tabs)
          ? saved.tabs.filter(isWorkspaceTab).filter((tab) => isAllowed(tab.path))
          : []
        const requestedActive = typeof saved.activePath === 'string' ? saved.activePath : ''

        this.tabs = restoredTabs
        this.activePath = restoredTabs.some((tab) => tab.path === requestedActive)
          ? requestedActive
          : restoredTabs[0]?.path || ''
        this.sidebarCollapsed = typeof saved.sidebarCollapsed === 'boolean'
          ? saved.sidebarCollapsed
          : false
        this.persist()
      } catch {
        window.localStorage.removeItem(STORAGE_KEY)
      }
    }
  }
})
