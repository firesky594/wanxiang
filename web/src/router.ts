import { createRouter, createWebHistory } from 'vue-router'
import Dashboard from './views/Dashboard.vue'
import Agents from './views/Agents.vue'
import TaskDetail from './views/TaskDetail.vue'
import MergeRequests from './views/MergeRequests.vue'
import Issues from './views/Issues.vue'
import AdminAccess from './views/AdminAccess.vue'
import Deliveries from './views/Deliveries.vue'
import Pipelines from './views/Pipelines.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/dashboard' },
    { path: '/login', component: AdminAccess, meta: { public: true } },
    { path: '/bootstrap', component: AdminAccess, meta: { public: true } },
    { path: '/init-manager', redirect: '/agents' },
    { path: '/dashboard', component: Dashboard },
    { path: '/agents', component: Agents },
    { path: '/tasks/:id', component: TaskDetail },
    { path: '/mrs', component: MergeRequests },
    { path: '/issues', component: Issues },
    { path: '/deliveries', component: Deliveries },
    { path: '/pipelines', component: Pipelines }
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
