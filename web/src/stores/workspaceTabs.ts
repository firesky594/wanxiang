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

const STORAGE_KEY = 'wanxiang_workspace_v1'

function isWorkspaceTab(value: unknown): value is WorkspaceTab {
  if (!value || typeof value !== 'object') return false
  const tab = value as Record<string, unknown>
  return typeof tab.path === 'string' && typeof tab.title === 'string'
}

export const useWorkspaceTabsStore = defineStore('workspaceTabs', {
  state: () => ({
    tabs: [] as WorkspaceTab[],
    activePath: '',
    sidebarCollapsed: true
  }),
  actions: {
    persist() {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify({
        tabs: this.tabs,
        activePath: this.activePath,
        sidebarCollapsed: this.sidebarCollapsed
      }))
    },
    openTab(tab: WorkspaceTab) {
      if (!this.tabs.some((item) => item.path === tab.path)) {
        this.tabs.push(tab)
      }
      this.activePath = tab.path
      this.persist()
    },
    activateTab(path: string) {
      if (!this.tabs.some((item) => item.path === path)) return
      this.activePath = path
      this.persist()
    },
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
    setSidebarCollapsed(value: boolean) {
      this.sidebarCollapsed = value
      this.persist()
    },
    restore(allowedPaths: Set<string>) {
      const raw = window.localStorage.getItem(STORAGE_KEY)
      if (!raw) return

      try {
        const saved = JSON.parse(raw) as PersistedWorkspace
        const restoredTabs = Array.isArray(saved.tabs)
          ? saved.tabs.filter(isWorkspaceTab).filter((tab) => allowedPaths.has(tab.path))
          : []
        const requestedActive = typeof saved.activePath === 'string' ? saved.activePath : ''

        this.tabs = restoredTabs
        this.activePath = restoredTabs.some((tab) => tab.path === requestedActive)
          ? requestedActive
          : restoredTabs[0]?.path || ''
        this.sidebarCollapsed = typeof saved.sidebarCollapsed === 'boolean'
          ? saved.sidebarCollapsed
          : true
        this.persist()
      } catch {
        window.localStorage.removeItem(STORAGE_KEY)
      }
    }
  }
})
