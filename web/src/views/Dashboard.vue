<template>
  <section class="console">
    <header class="topbar">
      <RouterLink class="brand" to="/dashboard">
        <span class="brand-mark"><el-icon><Cpu /></el-icon></span>
        <span>Wanxiang Agent</span>
      </RouterLink>
      <nav class="nav">
        <RouterLink to="/agents"><el-icon><Key /></el-icon>Agent 配置</RouterLink>
        <RouterLink to="/agents"><el-icon><Connection /></el-icon>Agents</RouterLink>
        <RouterLink to="/mrs"><el-icon><Share /></el-icon>MR</RouterLink>
        <RouterLink to="/issues"><el-icon><Warning /></el-icon>Issue</RouterLink>
      </nav>
    </header>

    <main class="main">
      <div class="page-head">
        <div>
          <h1>任务调度台</h1>
          <p>总管分配、agent 输出、测试反馈和人工阻塞都会通过事件流进入这里。</p>
        </div>
        <el-tag :type="events.connected ? 'success' : 'danger'" effect="plain">
          <span class="status-dot" :class="{ online: events.connected }"></span>
          {{ events.connected ? 'SSE 在线' : 'SSE 离线' }}
        </el-tag>
      </div>

      <section class="grid three">
        <div class="metric">
          <span class="muted">实时事件</span>
          <strong>{{ events.events.length }}</strong>
        </div>
        <div class="metric">
          <span class="muted">最后 actor</span>
          <strong class="mono">{{ lastEvent?.actor || '-' }}</strong>
        </div>
        <div class="metric">
          <span class="muted">最后类型</span>
          <strong class="mono">{{ lastEvent?.type || '-' }}</strong>
        </div>
      </section>

      <section class="grid two" style="margin-top: 16px">
        <div class="panel flow-shell">
          <WorkflowGraph :events="events.events" />
        </div>
        <div class="stack">
          <section class="panel stack">
            <h2>新建任务</h2>
            <el-input v-model="taskTitle" placeholder="任务标题" />
            <el-input
              v-model="taskDescription"
              type="textarea"
              :rows="5"
              placeholder="任务说明"
            />
            <el-button type="primary" :loading="creatingTask" @click="createTask">
              <el-icon><DocumentChecked /></el-icon>
              创建项目
            </el-button>
            <el-alert v-if="createdTask" type="success" :closable="false">
              <RouterLink :to="`/tasks/${createdTask.id}`" class="mono">
                {{ createdTask.project_slug }}
              </RouterLink>
            </el-alert>
          </section>
          <AgentOutputPanel :events="events.events" />
        </div>
      </section>
      <section class="panel stack" style="margin-top: 16px">
        <h2>持久任务</h2>
        <el-empty v-if="!tasks.loading && tasks.tasks.length === 0" description="尚无任务" />
        <RouterLink v-for="task in tasks.tasks" :key="task.id" :to="`/tasks/${task.id}`" class="mono">
          #{{ task.id }} {{ task.title }} · {{ task.status }}
        </RouterLink>
      </section>
    </main>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { Connection, Cpu, DocumentChecked, Key, Share, Warning } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { api, type Task } from '../api/client'
import AgentOutputPanel from '../components/AgentOutputPanel.vue'
import WorkflowGraph from '../components/WorkflowGraph.vue'
import { useEventsStore } from '../stores/events'
import { useTasksStore } from '../stores/tasks'

const events = useEventsStore()
const tasks = useTasksStore()
const taskTitle = ref('')
const taskDescription = ref('')
const creatingTask = ref(false)
const createdTask = ref<Task | null>(null)

const lastEvent = computed(() => events.events.at(-1))

onMounted(async () => {
  await tasks.loadList()
  events.connect()
})

async function createTask() {
  if (!taskTitle.value.trim()) {
    ElMessage.warning('请输入任务标题')
    return
  }
  creatingTask.value = true
  try {
    const res = await api<{ ok: boolean; task: Task }>('/api/admin/tasks', {
      method: 'POST',
      body: JSON.stringify({ title: taskTitle.value, description: taskDescription.value })
    })
    createdTask.value = res.task
    await tasks.loadList()
    ElMessage.success('任务项目已创建')
  } finally {
    creatingTask.value = false
  }
}
</script>
