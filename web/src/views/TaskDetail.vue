<template>
  <section class="console">
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
      <section v-if="workspace" class="panel stack workspace-panel" aria-labelledby="workspace-title">
        <div class="match-head">
          <div><span class="eyebrow">GIT ISOLATION</span><h2 id="workspace-title">隔离工作区</h2></div>
          <div class="workspace-actions">
            <span class="workspace-status mono" :class="`is-${workspace.status}`">{{ workspace.status }}</span>
            <el-button size="small" :loading="workspaceBusy" @click="reconcileWorkspace">重新校验</el-button>
          </div>
        </div>
        <el-empty v-if="workspace.items.length === 0" description="等待 Agent 匹配完成后创建工作区" />
        <article v-for="item in workspace.items" :key="item.id" class="workspace-row">
          <div class="workspace-rail"><span></span><small>#{{ item.step_id }}</small></div>
          <div class="workspace-copy">
            <div class="workspace-titleline"><strong>{{ item.agent_name }}</strong><span class="mono">{{ item.status }}</span></div>
            <code>{{ item.branch_name }}</code>
            <span class="mono path">{{ item.worktree_path }}</span>
            <dl>
              <div><dt>代码基线</dt><dd class="mono">{{ shortCommit(item.base_commit) }}</dd></div>
              <div><dt>分支起点</dt><dd class="mono">{{ shortCommit(item.provision_commit) }}</dd></div>
              <div><dt>汇报对象</dt><dd class="mono">{{ item.reports_to || 'manager' }}</dd></div>
            </dl>
            <el-alert v-if="item.last_error" type="error" :closable="false" :title="item.last_error" />
          </div>
        </article>
        <div v-if="workspace.status === 'drifted'" class="repair-bar">
          <div><strong>检测到数据库与 Git 现场不一致</strong><p>请选择可信来源，系统不会自动覆盖另一侧。</p></div>
          <el-button :loading="workspaceBusy" @click="repairWorkspace('database')">以数据库重建快照</el-button>
          <el-button :loading="workspaceBusy" @click="repairWorkspace('git_snapshot')">以 Git 快照恢复数据库</el-button>
        </div>
        <div v-if="workspace.items.length" class="cleanup-bar">
          <span>清理前会再次验证 worktree 归属；未知目录不会删除。</span>
          <el-button v-if="workspace.status !== 'cleanup_pending'" size="small" @click="requestCleanup">申请清理</el-button>
          <el-button v-else size="small" type="danger" @click="confirmCleanup">确认移除 worktree</el-button>
        </div>
      </section>
      <section v-if="leaseTimeline" class="panel stack recovery-panel" aria-labelledby="recovery-title">
        <div class="match-head">
          <div><span class="eyebrow">LEASE RECOVERY</span><h2 id="recovery-title">租约与断点恢复</h2></div>
          <el-button size="small" :loading="recoveryBusy" @click="loadRecovery">刷新时间线</el-button>
        </div>
        <el-empty v-if="leaseTimeline.steps.length === 0" description="等待工作包领取租约" />
        <article v-for="step in leaseTimeline.steps" :key="step.step_id" class="recovery-row">
          <div class="recovery-head">
            <div><strong>步骤 #{{ step.step_id }} · {{ step.agent_name }}</strong><span class="mono status-chip">{{ step.status }}</span></div>
            <span class="mono">attempt {{ step.attempt }} · lease v{{ step.lease_version }}</span>
          </div>
          <dl class="recovery-metrics">
            <div><dt>最后心跳</dt><dd>{{ formatTime(step.last_heartbeat_at) }}</dd></div>
            <div><dt>租约剩余</dt><dd>{{ remaining(step.lease_expires_at) }}</dd></div>
            <div><dt>恢复期限</dt><dd>{{ formatTime(step.resume_deadline) }}</dd></div>
            <div><dt>Checkpoint</dt><dd class="mono">{{ step.checkpoint_id ? `#${step.checkpoint_id}` : 'none' }}</dd></div>
          </dl>
          <div v-if="checkpointFor(step.step_id)" class="next-action">
            <span>下一动作</span><strong>{{ checkpointFor(step.step_id)?.summary.next_action }}</strong>
            <small v-if="checkpointFor(step.step_id)?.high_risk">高风险 checkpoint，接管前必须二次确认</small>
          </div>
          <div class="recovery-actions">
            <el-button v-if="step.status === 'interrupted'" size="small" @click="extendRecovery(step)">延长恢复期</el-button>
            <el-button v-if="leaseFor(step.step_id)?.status !== 'frozen'" size="small" type="warning" @click="freezeRecovery(step)">冻结</el-button>
            <el-button v-else size="small" @click="unfreezeRecovery(step)">解冻并换租约</el-button>
            <el-button v-if="step.status === 'interrupted' || step.status === 'blocked'" size="small" type="danger" @click="reassignRecovery(step)">立即接管</el-button>
          </div>
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
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import AgentOutputPanel from '../components/AgentOutputPanel.vue'
import WorkflowGraph from '../components/WorkflowGraph.vue'
import { api, cleanupTaskWorkspace, extendLeaseDeadline, freezeLease, getLeaseTimeline, getTaskMatch, getTaskWorkspace, overrideTaskMatch, reassignLease, reconcileTaskWorkspace, repairTaskWorkspace, unfreezeLease, type LeaseTimeline, type StepRecovery, type TaskMatch, type TaskWorkspace } from '../api/client'
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
const workspace = ref<TaskWorkspace | null>(null)
const workspaceBusy = ref(false)
const leaseTimeline = ref<LeaseTimeline | null>(null)
const recoveryBusy = ref(false)
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
const shortCommit = (value: string) => value ? value.slice(0, 10) : 'pending'
async function runWorkspace(action: () => Promise<TaskWorkspace>, message: string) { workspaceBusy.value = true; try { workspace.value = await action(); ElMessage.success(message) } finally { workspaceBusy.value = false } }
function reconcileWorkspace() { return runWorkspace(() => reconcileTaskWorkspace(taskID.value), '工作区校验完成') }
function repairWorkspace(direction: 'database'|'git_snapshot') { return runWorkspace(() => repairTaskWorkspace(taskID.value, direction), '漂移修复完成') }
async function requestCleanup() { await ElMessageBox.confirm('非终态任务也要申请清理吗？该操作不会立即删除 worktree。','申请清理',{type:'warning'}); await runWorkspace(() => cleanupTaskWorkspace(taskID.value,'request',true),'已进入待清理状态') }
async function confirmCleanup() { await ElMessageBox.confirm('确认移除已验证归属的 worktree？分支记录仍会保留。','确认清理',{type:'error'}); await runWorkspace(() => cleanupTaskWorkspace(taskID.value,'confirm'),'worktree 已清理') }
const leaseFor = (stepID: number) => leaseTimeline.value?.leases.find((lease) => lease.step_id === stepID)
const checkpointFor = (stepID: number) => leaseTimeline.value?.checkpoints.find((checkpoint) => checkpoint.step_id === stepID)
const formatTime = (value?: string) => value ? new Date(value).toLocaleString() : '—'
const remaining = (value?: string) => {
  if (!value) return '—'
  const seconds = Math.ceil((new Date(value).getTime() - Date.now()) / 1000)
  return seconds > 0 ? `${seconds}s` : '已到期'
}
async function loadRecovery() { recoveryBusy.value = true; try { leaseTimeline.value = await getLeaseTimeline(taskID.value) } finally { recoveryBusy.value = false } }
async function extendRecovery(step: StepRecovery) {
  const lease = leaseFor(step.step_id); if (!lease) return
  const deadline = new Date(Date.now() + 10 * 60_000).toISOString()
  await ElMessageBox.confirm(`将步骤 #${step.step_id} 的原 Agent 恢复期延长到 ${formatTime(deadline)}？`, '延长恢复期', { type: 'warning' })
  await extendLeaseDeadline(taskID.value, step.step_id, lease.lease_id, lease.lease_version, deadline); await loadRecovery(); ElMessage.success('恢复期限已延长')
}
async function freezeRecovery(step: StepRecovery) {
  await ElMessageBox.confirm(`冻结步骤 #${step.step_id} 会立即撤销当前写权限。`, '冻结工作包', { type: 'warning' })
  await freezeLease(taskID.value, step.step_id, '管理台人工冻结'); await loadRecovery(); ElMessage.success('工作包已冻结')
}
async function unfreezeRecovery(step: StepRecovery) {
  await ElMessageBox.confirm(`解冻步骤 #${step.step_id} 将生成新 lease，旧 lease 不会恢复。`, '解冻工作包', { type: 'warning' })
  await unfreezeLease(taskID.value, step.step_id); await loadRecovery(); ElMessage.success('已换发新租约')
}
async function reassignRecovery(step: StepRecovery) {
  const prompt = await ElMessageBox.prompt('输入接替 Agent 名称。系统将从干净 checkpoint 创建新分支和独立 worktree，原现场保持不变。', '立即接管', { confirmButtonText: '确认接管', cancelButtonText: '取消', inputPattern: /^[a-z0-9][a-z0-9-]{0,62}$/, inputErrorMessage: 'Agent 名称格式不正确', type: 'error' })
  const checkpoint = checkpointFor(step.step_id)
  if (checkpoint?.high_risk) await ElMessageBox.confirm('所选 checkpoint 标记为高风险。确认继续创建接力现场？', '高风险确认', { type: 'error' })
  await reassignLease(taskID.value, step.step_id, prompt.value.trim(), { checkpoint_id: checkpoint?.id, immediate: true, reason: '管理台立即接管' }); await loadRecovery(); workspace.value = await getTaskWorkspace(taskID.value); ElMessage.success('接力工作区已创建')
}

onMounted(async () => {
  await tasks.loadDetail(taskID.value)
  match.value = await getTaskMatch(taskID.value)
  workspace.value = await getTaskWorkspace(taskID.value)
  await loadRecovery()
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
.workspace-panel { margin-bottom: 16px; }.workspace-actions,.workspace-titleline,.cleanup-bar { display:flex;align-items:center;justify-content:space-between;gap:10px; }.workspace-status { padding:6px 10px;border-radius:999px;background:rgba(85,214,190,.1);color:#55d6be; }.workspace-status.is-drifted { color:#ff9f7a;background:rgba(255,110,80,.12); }.workspace-row { display:grid;grid-template-columns:48px 1fr;gap:12px;padding:17px 0;border-bottom:1px solid rgba(120,145,160,.16); }.workspace-rail { display:grid;justify-items:center;gap:5px; }.workspace-rail span { width:10px;height:10px;border:2px solid #55d6be;border-radius:50%;box-shadow:0 0 0 5px rgba(85,214,190,.08); }.workspace-copy { display:grid;gap:8px;min-width:0; }.workspace-copy code,.workspace-copy .path { overflow-wrap:anywhere; }.workspace-copy dl { display:grid;grid-template-columns:repeat(3,1fr);gap:8px;margin:0; }.workspace-copy dl div { padding:9px;background:rgba(12,24,31,.36);border-radius:8px; }.workspace-copy dt { font-size:11px;color:#8fa4ae; }.workspace-copy dd { margin:4px 0 0; }.repair-bar { display:grid;grid-template-columns:1fr auto auto;gap:10px;align-items:center;padding:14px;border:1px solid rgba(255,110,80,.35);background:rgba(255,110,80,.07);border-radius:12px; }.repair-bar p { margin:4px 0 0;color:#9eb0ba; }.cleanup-bar { padding-top:12px;color:#9eb0ba; }
.recovery-panel { margin-bottom:16px; }.recovery-row { display:grid;gap:12px;padding:18px 0;border-bottom:1px solid rgba(120,145,160,.16); }.recovery-row:last-child { border-bottom:0; }.recovery-head,.recovery-head>div,.recovery-actions { display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap; }.status-chip { color:#55d6be; }.recovery-metrics { display:grid;grid-template-columns:repeat(4,1fr);gap:8px;margin:0; }.recovery-metrics div,.next-action { padding:10px;background:rgba(12,24,31,.36);border-radius:8px; }.recovery-metrics dt,.next-action span { color:#8fa4ae;font-size:11px; }.recovery-metrics dd { margin:5px 0 0; }.next-action { display:grid;gap:5px; }.next-action small { color:#ff9f7a; }
@media (max-width: 820px) { .match-head { align-items: start; flex-direction: column; }.decision-row { grid-template-columns: 64px 1fr; }.override-form { grid-column: 1 / -1; }.workspace-copy dl,.recovery-metrics { grid-template-columns:1fr; }.repair-bar { grid-template-columns:1fr; }.cleanup-bar { align-items:flex-start;flex-direction:column; }.recovery-actions .el-button { width:100%;margin-left:0; } }
</style>
