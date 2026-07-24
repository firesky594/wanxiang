// @vitest-environment jsdom
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { useWorkspaceTabsStore } from '../../src/stores/workspaceTabs'

function createStorage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() {
      return values.size
    },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => values.delete(key),
    setItem: (key, value) => values.set(key, String(value))
  }
}

describe('workspace tabs store', () => {
  beforeEach(() => {
    Object.defineProperty(window, 'localStorage', {
      configurable: true,
      value: createStorage()
    })
    window.localStorage.clear()
    setActivePinia(createPinia())
  })

  it('starts without tabs and with the sidebar expanded', () => {
    const store = useWorkspaceTabsStore()

    expect(store.tabs).toEqual([])
    expect(store.activePath).toBe('')
    expect(store.sidebarCollapsed).toBe(false)
  })

  it('opens each path once and activates an existing tab', () => {
    const store = useWorkspaceTabsStore()

    store.openTab({ path: '/agents', title: 'Agents' })
    store.openTab({ path: '/agents', title: 'Agent 配置' })

    expect(store.tabs).toEqual([{ path: '/agents', title: 'Agents' }])
    expect(store.activePath).toBe('/agents')
  })

  it('selects the right neighbor, then the left neighbor, when closing tabs', () => {
    const store = useWorkspaceTabsStore()
    store.openTab({ path: '/agents', title: 'Agents' })
    store.openTab({ path: '/mrs', title: 'MR' })
    store.openTab({ path: '/issues', title: 'Issue' })
    store.activateTab('/mrs')

    expect(store.closeTab('/mrs')).toBe('/issues')
    expect(store.closeTab('/issues')).toBe('/agents')
    expect(store.closeTab('/agents')).toBe('')
    expect(store.activePath).toBe('')
  })

  it('restores valid routes and ignores unknown persisted paths', () => {
    window.localStorage.setItem('wanxiang_workspace_v2', JSON.stringify({
      tabs: [
        { path: '/agents', title: 'Agents' },
        { path: '/removed', title: '旧页面' }
      ],
      activePath: '/removed',
      sidebarCollapsed: false
    }))
    const store = useWorkspaceTabsStore()

    store.restore(new Set(['/agents', '/mrs']))

    expect(store.tabs).toEqual([{ path: '/agents', title: 'Agents' }])
    expect(store.activePath).toBe('/agents')
    expect(store.sidebarCollapsed).toBe(false)
  })
})
