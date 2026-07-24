<template>
  <section
    class="admin-shell"
    :class="{
      'sidebar-is-collapsed': tabs.sidebarCollapsed,
      'mobile-nav-is-open': mobileNavOpen
    }"
  >
    <aside class="admin-sidebar" aria-label="后台导航">
      <div class="sidebar-brand">
        <span class="brand-mark"><el-icon><Cpu /></el-icon></span>
        <strong v-if="!tabs.sidebarCollapsed || mobileNavOpen">Wanxiang Agent</strong>
      </div>

      <nav class="sidebar-nav">
        <button
          v-for="item in navigation"
          :key="item.path"
          type="button"
          :class="{ active: route.path === item.path }"
          :data-nav="item.path"
          :aria-label="item.title"
          @click="openNavigation(item)"
        >
          <el-icon><component :is="item.icon" /></el-icon>
          <span v-if="!tabs.sidebarCollapsed || mobileNavOpen" :data-testid="`nav-label-${item.name}`">
            {{ item.title }}
          </span>
        </button>
      </nav>

      <button
        type="button"
        class="sidebar-toggle"
        data-testid="sidebar-toggle"
        :aria-expanded="!tabs.sidebarCollapsed"
        :aria-label="tabs.sidebarCollapsed ? '展开导航' : '折叠导航'"
        @click="tabs.setSidebarCollapsed(!tabs.sidebarCollapsed)"
      >
        <el-icon><Expand v-if="tabs.sidebarCollapsed" /><Fold v-else /></el-icon>
        <span v-if="!tabs.sidebarCollapsed || mobileNavOpen">折叠导航</span>
      </button>
    </aside>

    <button
      v-if="mobileNavOpen"
      type="button"
      class="mobile-nav-backdrop"
      data-testid="mobile-nav-backdrop"
      aria-label="关闭导航"
      @click="mobileNavOpen = false"
    ></button>

    <section class="workspace">
      <div class="mobile-toolbar">
        <button
          type="button"
          data-testid="mobile-nav-toggle"
          :aria-expanded="mobileNavOpen"
          aria-label="打开导航"
          @click="mobileNavOpen = !mobileNavOpen"
        >
          <el-icon><Menu /></el-icon>
        </button>
        <strong>Wanxiang Agent</strong>
      </div>
      <header v-if="tabs.tabs.length" class="workspace-tabs" data-testid="workspace-tabs">
        <button
          v-for="tab in tabs.tabs"
          :key="tab.path"
          type="button"
          class="workspace-tab"
          :class="{ active: tabs.activePath === tab.path }"
          :data-tab="tab.path"
          @click="activateTab(tab.path)"
        >
          <span>{{ tab.title }}</span>
          <span
            role="button"
            tabindex="0"
            class="tab-close"
            :data-close-tab="tab.path"
            :aria-label="`关闭 ${tab.title}`"
            @click.stop="closeTab(tab.path)"
            @keydown.enter.stop="closeTab(tab.path)"
            @keydown.space.prevent.stop="closeTab(tab.path)"
          >
            <el-icon><Close /></el-icon>
          </span>
        </button>
      </header>

      <main class="workspace-content">
        <RouterView v-slot="{ Component, route: viewRoute }">
          <KeepAlive>
            <component :is="Component" :key="viewRoute.fullPath" />
          </KeepAlive>
        </RouterView>
      </main>
    </section>
  </section>
</template>

<script setup lang="ts">
import {
  Close,
  Connection,
  Cpu,
  DocumentChecked,
  Expand,
  Fold,
  Grid,
  Menu,
  Share,
  VideoPlay,
  Warning
} from '@element-plus/icons-vue'
import { computed, onMounted, ref, watch } from 'vue'
import { RouterView, useRoute, useRouter, type RouteRecordNormalized } from 'vue-router'
import { useWorkspaceTabsStore } from '../stores/workspaceTabs'

interface NavigationItem {
  name: string
  path: string
  title: string
  icon: typeof Grid
}

const iconMap: Record<string, typeof Grid> = {
  agents: Connection,
  dashboard: Grid,
  deliveries: DocumentChecked,
  issues: Warning,
  'merge-requests': Share,
  pipelines: VideoPlay
}

const router = useRouter()
const route = useRoute()
const tabs = useWorkspaceTabsStore()
const mobileNavOpen = ref(false)

const navigation = computed<NavigationItem[]>(() => router.getRoutes()
  .filter((item) => typeof item.meta.navOrder === 'number' && item.meta.navTitle)
  .sort((left, right) => Number(left.meta.navOrder) - Number(right.meta.navOrder))
  .map((item) => ({
    name: String(item.name),
    path: item.path,
    title: String(item.meta.navTitle),
    icon: iconMap[String(item.meta.navIcon)] || Grid
  })))

function routeTitle(record: RouteRecordNormalized) {
  if (record.meta.navTitle) return String(record.meta.navTitle)
  if (record.name === 'task-detail') return `任务 #${String(route.params.id)}`
  return String(record.name || route.path)
}

function syncRouteToTabs() {
  const record = route.matched.at(-1)
  if (!record?.meta.workspace || route.path === '/dashboard' && tabs.tabs.length === 0) return
  tabs.openTab({ path: route.fullPath, title: routeTitle(record) })
}

function isWorkspacePath(path: string) {
  const resolved = router.resolve(path)
  return resolved.matched.some((record) => record.meta.workspace)
}

async function openNavigation(item: NavigationItem) {
  tabs.openTab({ path: item.path, title: item.title })
  mobileNavOpen.value = false
  await router.push(item.path)
}

async function activateTab(path: string) {
  tabs.activateTab(path)
  await router.push(path)
}

async function closeTab(path: string) {
  const nextPath = tabs.closeTab(path)
  if (route.fullPath === path) {
    await router.push(nextPath || '/dashboard')
  }
}

onMounted(async () => {
  tabs.restore(isWorkspacePath)
  if (tabs.activePath && tabs.activePath !== route.fullPath) {
    await router.replace(tabs.activePath)
  } else {
    syncRouteToTabs()
  }
})

watch(() => route.fullPath, syncRouteToTabs)
</script>

<style scoped>
.admin-shell {
  display: grid;
  grid-template-columns: 220px minmax(0, 1fr);
  min-height: 100vh;
}

.admin-shell.sidebar-is-collapsed {
  grid-template-columns: 72px minmax(0, 1fr);
}

.admin-sidebar {
  display: flex;
  flex-direction: column;
  min-width: 0;
  padding: 16px 10px;
  color: #eaf5f0;
  background: #1d2d29;
}

.sidebar-brand,
.sidebar-nav button,
.sidebar-toggle {
  display: flex;
  align-items: center;
  gap: 10px;
}

.sidebar-brand {
  min-height: 42px;
  margin-bottom: 22px;
  padding: 0 6px;
}

.sidebar-brand strong {
  white-space: nowrap;
}

.sidebar-nav {
  display: grid;
  gap: 7px;
}

.sidebar-nav button,
.sidebar-toggle {
  width: 100%;
  min-height: 42px;
  padding: 0 12px;
  border: 0;
  border-radius: 7px;
  color: inherit;
  background: transparent;
  cursor: pointer;
}

.sidebar-nav button:hover,
.sidebar-nav button.active,
.sidebar-toggle:hover {
  background: #2f7d68;
}

.sidebar-toggle {
  margin-top: auto;
}

.workspace {
  min-width: 0;
}

.mobile-toolbar,
.mobile-nav-backdrop {
  display: none;
}

.workspace-tabs {
  display: flex;
  gap: 6px;
  min-height: 52px;
  padding: 9px 14px;
  overflow-x: auto;
  border-bottom: 1px solid var(--wx-line);
  background: #f9fbf8;
}

.workspace-tab {
  display: inline-flex;
  align-items: center;
  gap: 9px;
  flex: none;
  padding: 0 11px;
  border: 1px solid var(--wx-line);
  border-radius: 6px;
  color: var(--wx-muted);
  background: #fff;
  cursor: pointer;
}

.workspace-tab.active {
  border-color: var(--wx-green);
  color: var(--wx-ink);
}

.tab-close {
  display: inline-grid;
  place-items: center;
}

@media (max-width: 767px) {
  .admin-shell,
  .admin-shell.sidebar-is-collapsed {
    grid-template-columns: minmax(0, 1fr);
  }

  .admin-sidebar {
    position: fixed;
    inset: 0 auto 0 0;
    z-index: 30;
    width: 220px;
    transform: translateX(-100%);
    transition: transform 180ms ease;
  }

  .mobile-nav-is-open .admin-sidebar {
    transform: translateX(0);
  }

  .mobile-nav-backdrop {
    position: fixed;
    inset: 0;
    z-index: 20;
    display: block;
    border: 0;
    background: rgba(18, 30, 26, 0.48);
  }

  .mobile-toolbar {
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 52px;
    padding: 8px 14px;
    border-bottom: 1px solid var(--wx-line);
    background: #fff;
  }

  .mobile-toolbar button {
    display: inline-grid;
    place-items: center;
    width: 36px;
    height: 36px;
    border: 1px solid var(--wx-line);
    border-radius: 6px;
    background: #fff;
    cursor: pointer;
  }
}
</style>
