// @vitest-environment jsdom
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it } from 'vitest'
import AdminShell from './AdminShell.vue'
import { useWorkspaceTabsStore } from '../stores/workspaceTabs'

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

const DashboardStub = defineComponent({ template: '<div data-page="dashboard">Dashboard 内容</div>' })
const AgentsStub = defineComponent({ template: '<div data-page="agents">Agents 内容</div>' })

async function mountShell() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: '/dashboard',
        name: 'dashboard',
        component: DashboardStub,
        meta: { workspace: true, navTitle: '调度台', navIcon: 'dashboard', navOrder: 10 }
      },
      {
        path: '/agents',
        name: 'agents',
        component: AgentsStub,
        meta: { workspace: true, navTitle: 'Agents', navIcon: 'agents', navOrder: 20 }
      }
    ]
  })
  await router.push('/dashboard')
  await router.isReady()
  const wrapper = mount(AdminShell, {
    global: {
      plugins: [pinia, router],
      stubs: {
        'el-icon': true
      }
    }
  })
  await flushPromises()
  return { wrapper, router, store: useWorkspaceTabsStore() }
}

describe('admin shell', () => {
  beforeEach(() => {
    Object.defineProperty(window, 'localStorage', {
      configurable: true,
      value: createStorage()
    })
  })

  it('shows icon-only navigation until the sidebar is expanded', async () => {
    const { wrapper } = await mountShell()
    const toggle = wrapper.get('[data-testid="sidebar-toggle"]')

    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('[data-testid="nav-label-agents"]').exists()).toBe(false)

    await toggle.trigger('click')

    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('[data-testid="nav-label-agents"]').text()).toBe('Agents')
  })

  it('opens one tab per navigation path and activates its content', async () => {
    const { wrapper, store } = await mountShell()

    await wrapper.get('[data-nav="/agents"]').trigger('click')
    await wrapper.get('[data-nav="/agents"]').trigger('click')
    await flushPromises()

    expect(store.tabs).toEqual([{ path: '/agents', title: 'Agents' }])
    expect(wrapper.get('[data-tab="/agents"]').text()).toContain('Agents')
    expect(wrapper.get('[data-page="agents"]').exists()).toBe(true)
  })

  it('shows the unlabelled dashboard after the last tab is closed', async () => {
    const { wrapper, router, store } = await mountShell()
    await wrapper.get('[data-nav="/agents"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-close-tab="/agents"]').trigger('click')
    await flushPromises()

    expect(store.tabs).toEqual([])
    expect(router.currentRoute.value.path).toBe('/dashboard')
    expect(wrapper.find('[data-testid="workspace-tabs"]').exists()).toBe(false)
    expect(wrapper.get('[data-page="dashboard"]').exists()).toBe(true)
  })
})
