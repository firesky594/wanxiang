import { createRouter, createWebHistory } from 'vue-router'
import Dashboard from './views/Dashboard.vue'
import Agents from './views/Agents.vue'
import TaskDetail from './views/TaskDetail.vue'
import MergeRequests from './views/MergeRequests.vue'
import Issues from './views/Issues.vue'
import AdminAccess from './views/AdminAccess.vue'
import Deliveries from './views/Deliveries.vue'
import Pipelines from './views/Pipelines.vue'

declare module 'vue-router' {
  interface RouteMeta {
    public?: boolean
    workspace?: boolean
    navTitle?: string
    navIcon?: string
    navOrder?: number
  }
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/dashboard' },
    { path: '/login', component: AdminAccess, meta: { public: true } },
    { path: '/bootstrap', component: AdminAccess, meta: { public: true } },
    { path: '/init-manager', redirect: '/agents' },
    { path: '/dashboard', name: 'dashboard', component: Dashboard, meta: { workspace: true, navTitle: '调度台', navIcon: 'dashboard', navOrder: 10 } },
    { path: '/agents', name: 'agents', component: Agents, meta: { workspace: true, navTitle: 'Agents', navIcon: 'agents', navOrder: 20 } },
    { path: '/tasks/:id', name: 'task-detail', component: TaskDetail, meta: { workspace: true } },
    { path: '/mrs', name: 'merge-requests', component: MergeRequests, meta: { workspace: true, navTitle: 'MR', navIcon: 'merge-requests', navOrder: 30 } },
    { path: '/deliveries', name: 'deliveries', component: Deliveries, meta: { workspace: true, navTitle: '交付验收', navIcon: 'deliveries', navOrder: 40 } },
    { path: '/pipelines', name: 'pipelines', component: Pipelines, meta: { workspace: true, navTitle: '流水线', navIcon: 'pipelines', navOrder: 50 } },
    { path: '/issues', name: 'issues', component: Issues, meta: { workspace: true, navTitle: 'Issue', navIcon: 'issues', navOrder: 60 } }
  ]
})

router.beforeEach((to) => {
  const token = localStorage.getItem('wanxiang_admin_token')
  if (!to.meta.public && !token) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  if (to.meta.public && token) {
    return '/dashboard'
  }
})
