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
      <section v-if="match" class="panel stack match-panel" aria-labelledby="match-title">
        <div class="match-head">
          <div>
            <span class="eyebrow">MATCH TRACE</span>
            <h2 id="match-title">Agent 匹配决策</h2>
          </div>
          <span class="mono lead-chip">{{ match.requires_lead ? `负责人 ${match.project_lead}` : '无需独立负责人' }}</span>
        </div>
        <article v-for="decision in latestDecisions" :key="decision.id" class="decision-row">
          <div class="decision-score"><strong>{{ decision.score.toFixed(1) }}</strong><small>匹配分</small></div>
          <div class="decision-copy">
            <strong>步骤 #{{ decision.step_id }} · {{ decision.selected_agent || '等待配置' }}</strong>
            <span class="mono">{{ decision.reasons.join(' · ') || decision.status }}</span>
            <details v-if="decision.rejections.length">
              <summary>{{ decision.rejections.length }} 个候选被过滤</summary>
              <p v-for="rejection in decision.rejections" :key="rejection.name" class="mono">
                {{ rejection.name }} — {{ rejection.reasons.join(', ') }}
              </p>
            </details>
          </div>
          <form class="override-form" @submit.prevent="override(decision.step_id)">
            <label :for="`agent-${decision.step_id}`">覆盖 Agent</label>
            <el-input :id="`agent-${decision.step_id}`" v-model="overrideAgents[decision.step_id]" placeholder="在线 Agent 名称" />
            <el-button native-type="submit" :loading="overridingStep === decision.step_id">应用</el-button>
          </form>
        </article>
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
import { computed, onMounted, reactive, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { ArrowRight, Cpu, Share } from '@element-plus/icons-vue'
import AgentOutputPanel from '../components/AgentOutputPanel.vue'
import WorkflowGraph from '../components/WorkflowGraph.vue'
import { api, getTaskMatch, overrideTaskMatch, type TaskMatch } from '../api/client'
import { useEventsStore, type RuntimeEvent } from '../stores/events'
import { useTasksStore } from '../stores/tasks'

const route = useRoute()
const events = useEventsStore()
const tasks = useTasksStore()
const taskID = computed(() => Number(route.params.id))
const taskEvents = computed(() => events.events.filter((event) => event.task_id === taskID.value))
const match = ref<TaskMatch | null>(null)
const overrideAgents = reactive<Record<number, string>>({})
const overridingStep = ref<number | null>(null)
const latestDecisions = computed(() => {
  const byStep = new Map<number, TaskMatch['decisions'][number]>()
  for (const decision of match.value?.decisions || []) byStep.set(decision.step_id, decision)
  return [...byStep.values()]
})

async function override(stepID: number) {
  const agentName = overrideAgents[stepID]?.trim()
  if (!agentName) return
  overridingStep.value = stepID
  try { match.value = await overrideTaskMatch(taskID.value, stepID, agentName) }
  finally { overridingStep.value = null }
}

onMounted(async () => {
  await tasks.loadDetail(taskID.value)
  match.value = await getTaskMatch(taskID.value)
  const response = await api<{ ok: boolean; events: RuntimeEvent[] }>(`/api/admin/tasks/${taskID.value}/events?limit=100&offset=0`)
  events.hydrate(response.events)
  events.connect()
})
</script>

<style scoped>
.match-panel { margin-bottom: 16px; overflow: hidden; }
.match-head { display: flex; justify-content: space-between; gap: 16px; align-items: end; border-bottom: 1px solid rgba(120, 145, 160, .22); padding-bottom: 14px; }
.match-head h2 { margin: 3px 0 0; }
.eyebrow { color: #55d6be; font: 700 11px/1 monospace; letter-spacing: .18em; }
.lead-chip { border: 1px solid rgba(85, 214, 190, .35); border-radius: 999px; padding: 7px 11px; }
.decision-row { display: grid; grid-template-columns: 76px minmax(0, 1fr) minmax(210px, 280px); gap: 18px; align-items: center; padding: 18px 0; border-bottom: 1px solid rgba(120, 145, 160, .16); }
.decision-row:last-child { border-bottom: 0; }
.decision-score { width: 64px; height: 64px; display: grid; place-content: center; text-align: center; border: 1px solid rgba(85, 214, 190, .45); border-radius: 18px 6px 18px 6px; background: rgba(85, 214, 190, .08); }
.decision-score strong { font-size: 20px; }.decision-score small { opacity: .66; }
.decision-copy { display: grid; gap: 7px; min-width: 0; }.decision-copy details { color: #9eb0ba; }.decision-copy p { margin: 7px 0 0; }
.override-form { display: grid; grid-template-columns: 1fr auto; gap: 7px; align-items: end; }.override-form label { grid-column: 1 / -1; font-size: 12px; opacity: .7; }
@media (max-width: 820px) { .match-head { align-items: start; flex-direction: column; }.decision-row { grid-template-columns: 64px 1fr; }.override-form { grid-column: 1 / -1; } }
</style>
