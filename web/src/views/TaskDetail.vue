<template>
  <section class="console">
    <header class="topbar">
      <RouterLink class="brand" to="/dashboard">
        <span class="brand-mark"><el-icon><Cpu /></el-icon></span>
        <span>Wanxiang Agent</span>
      </RouterLink>
      <nav class="nav">
        <RouterLink to="/dashboard"><el-icon><ArrowRight /></el-icon>调度台</RouterLink>
        <RouterLink to="/mrs"><el-icon><Share /></el-icon>MR</RouterLink>
      </nav>
    </header>
    <main class="main">
      <div class="page-head">
        <div>
          <h1>任务 #{{ taskID }}</h1>
          <p>实时查看该任务的调度路径、agent 输出、文件和 token 事件。</p>
        </div>
      </div>
      <section v-if="tasks.detail" class="panel stack" style="margin-bottom: 16px">
        <strong>{{ tasks.detail.task.title }}</strong>
        <span class="mono">{{ tasks.detail.project.slug }} · {{ tasks.detail.task.status }}</span>
      </section>
      <section class="grid two">
        <div class="panel flow-shell">
          <WorkflowGraph :events="taskEvents" />
        </div>
        <AgentOutputPanel :events="taskEvents" />
      </section>
    </main>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { ArrowRight, Cpu, Share } from '@element-plus/icons-vue'
import AgentOutputPanel from '../components/AgentOutputPanel.vue'
import WorkflowGraph from '../components/WorkflowGraph.vue'
import { api } from '../api/client'
import { useEventsStore, type RuntimeEvent } from '../stores/events'
import { useTasksStore } from '../stores/tasks'

const route = useRoute()
const events = useEventsStore()
const tasks = useTasksStore()
const taskID = computed(() => Number(route.params.id))
const taskEvents = computed(() => events.events.filter((event) => event.task_id === taskID.value))

onMounted(async () => {
  await tasks.loadDetail(taskID.value)
  const response = await api<{ ok: boolean; events: RuntimeEvent[] }>(`/api/admin/tasks/${taskID.value}/events?limit=100&offset=0`)
  events.hydrate(response.events)
  events.connect()
})
</script>
