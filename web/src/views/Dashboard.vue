<template>
  <section class="dispatch-canvas" data-testid="dispatch-canvas">
    <AgentCanvas
      ref="agentCanvas"
      :agents="agents"
      :loading="agentsLoading && agents.length === 0"
      @select-agent="openAgentConfig"
    />

    <header class="canvas-title">
      <span class="canvas-kicker">LIVE ORCHESTRATION</span>
      <h1>任务调度台</h1>
      <p>
        <strong>{{ connectedAgentCount }}</strong> 个 Agent 已连接
        <span>/</span>
        共 {{ agents.length }} 个
      </p>
    </header>

    <div class="canvas-toolbar" role="toolbar" aria-label="任务调度台操作">
      <span class="sse-state" :class="{ connected: events.connected }">
        <i></i>
        {{ events.connected ? 'SSE 在线' : 'SSE 离线' }}
      </span>
      <el-button
        plain
        :loading="agentsLoading"
        aria-label="刷新 Agent 状态"
        @click="refreshAgents(true)"
      >
        <el-icon><Refresh /></el-icon>
        <span class="toolbar-label">刷新</span>
      </el-button>
      <el-button plain aria-label="重置 Agent 画布位置" @click="resetAgentLayout">
        <el-icon><Aim /></el-icon>
        <span class="toolbar-label">重置布局</span>
      </el-button>
      <el-button plain aria-label="打开任务列表" @click="openTaskList">
        <el-icon><Tickets /></el-icon>
        <span class="toolbar-label">任务列表</span>
      </el-button>
      <el-button type="primary" aria-label="新建任务" @click="openTaskComposer">
        <el-icon><Plus /></el-icon>
        <span class="toolbar-label">新建任务</span>
      </el-button>
    </div>

    <div v-if="agentError" class="canvas-error" role="alert">
      {{ agentError }}
    </div>

    <div class="canvas-readout" aria-hidden="true">
      <span>AGENT FIELD</span>
      <b>{{ String(connectedAgentCount).padStart(2, '0') }}/{{ String(agents.length).padStart(2, '0') }}</b>
    </div>

    <el-drawer
      v-model="drawerOpen"
      class="control-drawer"
      :title="drawerTitle"
      :size="drawerSize"
      append-to-body
      destroy-on-close
      @closed="handleDrawerClosed"
    >
      <AgentConfigPanel
        v-if="drawerMode === 'agent'"
        :agent="selectedAgent"
        @updated="handleAgentConfigUpdated"
      />

      <div v-else-if="drawerMode === 'create'" class="task-composer">
        <p>提交任务后，总管会创建或复用隔离项目并进入调度流程。</p>
        <el-input v-model="taskTitle" placeholder="任务标题" />
        <el-input
          v-model="taskDescription"
          type="textarea"
          :rows="7"
          placeholder="任务说明"
        />
        <div class="project-choice">
          <label for="project-mode">项目范围</label>
          <el-radio-group id="project-mode" v-model="projectMode">
            <el-radio-button value="new">新建隔离项目</el-radio-button>
            <el-radio-button value="existing">复用已有项目</el-radio-button>
          </el-radio-group>
          <el-select
            v-if="projectMode === 'existing'"
            v-model="selectedProjectID"
            placeholder="选择一个干净的 main 项目"
            filterable
          >
            <el-option
              v-for="project in projects"
              :key="project.id"
              :value="project.id"
              :label="`${project.slug} · #${project.id}`"
            />
          </el-select>
          <small>后端仍会校验项目路径、分支和工作区状态。</small>
        </div>
        <el-button
          type="primary"
          class="full-width"
          :loading="creatingTask"
          @click="createTask"
        >
          <el-icon><DocumentChecked /></el-icon>
          创建项目
        </el-button>
      </div>

      <div
        v-else-if="drawerMode === 'tasks'"
        v-loading="tasks.loading"
        class="task-list"
      >
        <el-alert
          v-if="tasks.error"
          :title="tasks.error"
          type="error"
          :closable="false"
          show-icon
        />
        <el-empty
          v-if="!tasks.loading && !tasks.error && tasks.tasks.length === 0"
          description="尚无持久任务"
        />
        <RouterLink
          v-for="task in tasks.tasks"
          :key="task.id"
          :to="`/tasks/${task.id}`"
          class="task-link"
          @click="drawerOpen = false"
        >
          <span class="task-number">#{{ task.id }}</span>
          <span>
            <strong>{{ task.title }}</strong>
            <small>{{ task.project_slug }}</small>
          </span>
          <i>{{ task.status }}</i>
        </RouterLink>
      </div>
    </el-drawer>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { Aim, DocumentChecked, Plus, Refresh, Tickets } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import {
  api,
  createAdminTask,
  listAgentConfigs,
  type AgentConfig,
  type Project
} from '../api/client'
import AgentCanvas from '../components/AgentCanvas.vue'
import AgentConfigPanel from '../components/AgentConfigPanel.vue'
import { useEventsStore } from '../stores/events'
import { useTasksStore } from '../stores/tasks'

type DrawerMode = 'agent' | 'create' | 'tasks'
type PendingTaskSubmission = {
  version: 1
  idempotencyKey: string
  fingerprint: string
}

const pendingTaskSubmissionStorageKey = 'wanxiang.task-create-pending.v1'
const pendingTaskSubmission = readPendingTaskSubmission()

const events = useEventsStore()
const tasks = useTasksStore()
const agentCanvas = ref<InstanceType<typeof AgentCanvas>>()
const agents = ref<AgentConfig[]>([])
const agentsLoading = ref(false)
const agentError = ref('')
const taskTitle = ref('')
const taskDescription = ref('')
const creatingTask = ref(false)
const taskIdempotencyKey = ref(pendingTaskSubmission?.idempotencyKey || createTaskIdempotencyKey())
const taskSubmissionFingerprint = ref(pendingTaskSubmission?.fingerprint || '')
const projects = ref<Project[]>([])
const projectMode = ref<'new' | 'existing'>('new')
const selectedProjectID = ref<number>()
const drawerOpen = ref(false)
const drawerMode = ref<DrawerMode>('create')
const selectedAgentName = ref('')
const drawerSize = ref(430)
let agentRefreshTimer: number | undefined

const selectedAgent = computed(() =>
  agents.value.find((agent) => agent.name === selectedAgentName.value) || null)

const drawerTitle = computed(() => {
  if (drawerMode.value === 'agent') {
    return selectedAgent.value ? `Agent 配置 · ${selectedAgent.value.name}` : 'Agent 配置'
  }
  return drawerMode.value === 'create' ? '创建新的调度任务' : '持久任务'
})

const connectedAgentCount = computed(() =>
  agents.value.filter((agent) => ['online', 'busy'].includes(agent.status.trim().toLowerCase())).length)

/** 生成兼容非安全上下文浏览器的单次任务提交幂等键。 */
function createTaskIdempotencyKey() {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return globalThis.crypto.randomUUID()
  }
  return `task-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

/** 读取刷新前尚未确认成功的任务提交标识，不保存任务标题或正文。 */
function readPendingTaskSubmission(): PendingTaskSubmission | null {
  if (typeof window === 'undefined') return null
  try {
    const raw = window.sessionStorage.getItem(pendingTaskSubmissionStorageKey)
    if (!raw) return null
    const value = JSON.parse(raw) as Partial<PendingTaskSubmission>
    if (
      value.version !== 1 ||
      typeof value.idempotencyKey !== 'string' ||
      value.idempotencyKey.length < 8 ||
      value.idempotencyKey.length > 200 ||
      typeof value.fingerprint !== 'string' ||
      value.fingerprint.length === 0 ||
      value.fingerprint.length > 256
    ) {
      window.sessionStorage.removeItem(pendingTaskSubmissionStorageKey)
      return null
    }
    return value as PendingTaskSubmission
  } catch {
    return null
  }
}

/** 保存本标签页待确认提交的幂等键和指纹，支持响应丢失后的刷新重试。 */
function persistPendingTaskSubmission(idempotencyKey: string, fingerprint: string) {
  if (typeof window === 'undefined') return
  try {
    window.sessionStorage.setItem(
      pendingTaskSubmissionStorageKey,
      JSON.stringify({ version: 1, idempotencyKey, fingerprint } satisfies PendingTaskSubmission)
    )
  } catch {
    // 浏览器禁用存储时仍由服务端与当前页面内存幂等保护。
  }
}

/** 清除已经收到成功响应的任务提交标识。 */
function clearPendingTaskSubmission() {
  if (typeof window === 'undefined') return
  try {
    window.sessionStorage.removeItem(pendingTaskSubmissionStorageKey)
  } catch {
    // 忽略浏览器存储不可用，避免影响已成功的任务流程。
  }
}

/** 将抽屉宽度同步为 Element Plus 可解析的响应式像素值。 */
function syncDrawerSize() {
  drawerSize.value = Math.min(430, Math.floor(window.innerWidth * 0.94))
}

/** 拉取最新 Agent 列表并刷新连接状态。 */
async function refreshAgents(showFeedback = false) {
  if (agentsLoading.value) return
  agentsLoading.value = true
  agentError.value = ''
  try {
    agents.value = await listAgentConfigs()
    if (showFeedback) ElMessage.success('Agent 状态已刷新')
  } catch (error) {
    agentError.value = error instanceof Error ? error.message : 'Agent 状态加载失败'
    if (showFeedback) ElMessage.error('Agent 状态刷新失败')
  } finally {
    agentsLoading.value = false
  }
}

/** 初始化任务、项目、Agent 和实时事件数据。 */
async function loadDashboardData() {
  try {
    const [, projectResponse] = await Promise.all([
      tasks.loadList(),
      api<{ ok: boolean; projects: Project[] }>('/api/admin/projects?limit=100&offset=0'),
      refreshAgents()
    ])
    projects.value = projectResponse.projects
  } catch (error) {
    agentError.value = error instanceof Error ? error.message : '调度台数据加载失败'
  }
  events.connect()
}

/** 打开新建任务抽屉并保留尚未提交成功的表单。 */
function openTaskComposer() {
  drawerMode.value = 'create'
  selectedAgentName.value = ''
  drawerOpen.value = true
}

/** 打开持久任务抽屉。 */
function openTaskList() {
  drawerMode.value = 'tasks'
  selectedAgentName.value = ''
  drawerOpen.value = true
  void tasks.loadList().catch((error) => {
    agentError.value = error instanceof Error ? error.message : '任务列表加载失败'
  })
}

/** 打开用户所选 Agent 的脱敏配置与编辑面板。 */
function openAgentConfig(agent: AgentConfig) {
  selectedAgentName.value = agent.name
  drawerMode.value = 'agent'
  drawerOpen.value = true
}

/** Agent 配置操作结束后重新同步画布状态。 */
async function handleAgentConfigUpdated() {
  await refreshAgents()
}

/** 抽屉关闭后清除 Agent 选择，避免保留密钥输入组件。 */
function handleDrawerClosed() {
  if (drawerMode.value === 'agent') selectedAgentName.value = ''
}

/** 清除用户保存的位置并恢复 Agent 默认布局。 */
async function resetAgentLayout() {
  await agentCanvas.value?.resetLayout()
  ElMessage.success('Agent 布局已重置')
}

/** 重置任务创建表单并生成下一次提交使用的幂等键。 */
function resetTaskComposer() {
  clearPendingTaskSubmission()
  taskTitle.value = ''
  taskDescription.value = ''
  projectMode.value = 'new'
  selectedProjectID.value = undefined
  taskIdempotencyKey.value = createTaskIdempotencyKey()
  taskSubmissionFingerprint.value = ''
}

/** 计算不含任务正文的稳定摘要，用于区分原样重试和编辑后的新请求。 */
async function currentTaskSubmissionFingerprint() {
  const payload = JSON.stringify({
    title: taskTitle.value.trim(),
    description: taskDescription.value,
    project_id: projectMode.value === 'existing' ? selectedProjectID.value : null
  })
  const bytes = new TextEncoder().encode(payload)
  if (typeof globalThis.crypto?.subtle?.digest === 'function') {
    const digest = await globalThis.crypto.subtle.digest('SHA-256', bytes)
    return `sha256:${Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, '0')).join('')}`
  }
  return fallbackTaskSubmissionFingerprint(bytes)
}

/** 在缺少 Web Crypto 时生成不含原文的多路稳定摘要。 */
function fallbackTaskSubmissionFingerprint(bytes: Uint8Array) {
  const hashes = [0x811c9dc5, 0x9e3779b9, 0x85ebca6b, 0xc2b2ae35]
  for (const value of bytes) {
    for (let index = 0; index < hashes.length; index += 1) {
      hashes[index] = Math.imul(hashes[index] ^ (value + index * 17), 0x01000193)
    }
  }
  return `fnv128:${hashes.map((value) => (value >>> 0).toString(16).padStart(8, '0')).join('')}`
}

/** 防重提交任务，成功时关闭重置，失败时保留用户输入。 */
async function createTask() {
  if (creatingTask.value) return
  if (!taskTitle.value.trim()) {
    ElMessage.warning('请输入任务标题')
    return
  }
  if (projectMode.value === 'existing' && selectedProjectID.value === undefined) {
    ElMessage.warning('请选择要复用的项目')
    return
  }
  creatingTask.value = true
  try {
    const fingerprint = await currentTaskSubmissionFingerprint()
    if (taskSubmissionFingerprint.value && taskSubmissionFingerprint.value !== fingerprint) {
      taskIdempotencyKey.value = createTaskIdempotencyKey()
    }
    taskSubmissionFingerprint.value = fingerprint
    persistPendingTaskSubmission(taskIdempotencyKey.value, fingerprint)
    await createAdminTask(
      taskTitle.value,
      taskDescription.value,
      projectMode.value === 'existing' ? selectedProjectID.value : undefined,
      taskIdempotencyKey.value
    )
    drawerOpen.value = false
    resetTaskComposer()
    ElMessage.success('任务项目已创建')
    void tasks.loadList().catch(() => {
      ElMessage.warning('任务已创建，但任务列表刷新失败')
    })
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '任务项目创建失败')
  } finally {
    creatingTask.value = false
  }
}

onMounted(async () => {
  syncDrawerSize()
  window.addEventListener('resize', syncDrawerSize)
  await loadDashboardData()
  agentRefreshTimer = window.setInterval(() => void refreshAgents(), 15_000)
})

onBeforeUnmount(() => {
  if (agentRefreshTimer !== undefined) window.clearInterval(agentRefreshTimer)
  window.removeEventListener('resize', syncDrawerSize)
})
</script>

<style scoped>
.dispatch-canvas {
  position: relative;
  isolation: isolate;
  width: 100%;
  height: calc(100dvh - var(--workspace-chrome-height, 0px));
  overflow: hidden;
  color: #eafff8;
  background: #071310;
}

.canvas-title {
  position: absolute;
  top: 24px;
  left: 26px;
  z-index: 8;
  pointer-events: none;
}

.canvas-kicker {
  display: block;
  margin-bottom: 7px;
  color: rgba(131, 234, 204, 0.65);
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  letter-spacing: 0.18em;
}

.canvas-title h1 {
  margin: 0;
  font-size: clamp(25px, 3vw, 42px);
  line-height: 1;
  letter-spacing: -0.04em;
}

.canvas-title p {
  display: flex;
  align-items: baseline;
  gap: 7px;
  margin: 9px 0 0;
  color: rgba(212, 239, 231, 0.62);
  font-size: 12px;
}

.canvas-title p strong {
  color: #67e1bd;
  font-size: 20px;
}

.canvas-toolbar {
  position: absolute;
  top: 20px;
  right: 22px;
  z-index: 9;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px;
  border: 1px solid rgba(137, 225, 199, 0.16);
  border-radius: 12px;
  background: rgba(7, 24, 19, 0.82);
  box-shadow: 0 16px 38px rgba(0, 0, 0, 0.24);
  backdrop-filter: blur(14px);
}

.canvas-toolbar :deep(.el-button) {
  margin-left: 0;
  border-color: rgba(140, 219, 197, 0.2);
  color: #cce9df;
  background: rgba(255, 255, 255, 0.035);
}

.canvas-toolbar :deep(.el-button:hover),
.canvas-toolbar :deep(.el-button:focus-visible) {
  border-color: #62dcb9;
  color: #effff9;
  background: rgba(98, 220, 185, 0.12);
}

.canvas-toolbar :deep(.el-button--primary) {
  border-color: #2f9f82;
  color: #071310;
  background: #67dfbd;
}

.sse-state {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 0 9px;
  color: #dd8379;
  font-size: 11px;
  font-weight: 720;
  white-space: nowrap;
}

.sse-state i {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #e05248;
  box-shadow: 0 0 12px rgba(224, 82, 72, 0.72);
}

.sse-state.connected {
  color: #76e2c2;
}

.sse-state.connected i {
  background: #3fe09f;
  box-shadow: 0 0 12px rgba(63, 224, 159, 0.78);
}

.canvas-error {
  position: absolute;
  top: 126px;
  left: 26px;
  z-index: 9;
  max-width: min(520px, calc(100% - 52px));
  padding: 10px 13px;
  border: 1px solid rgba(237, 102, 91, 0.36);
  border-radius: 9px;
  color: #ffd2cd;
  background: rgba(87, 24, 20, 0.82);
}

.canvas-readout {
  position: absolute;
  bottom: 18px;
  left: 22px;
  z-index: 8;
  display: flex;
  align-items: center;
  gap: 10px;
  color: rgba(188, 226, 215, 0.42);
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  letter-spacing: 0.12em;
  pointer-events: none;
}

.canvas-readout b {
  color: #65dab8;
  font-size: 14px;
}

.task-composer,
.task-list {
  display: grid;
  gap: 16px;
}

.task-composer > p {
  margin: 0;
  color: var(--wx-muted);
  line-height: 1.65;
}

.project-choice {
  display: grid;
  gap: 10px;
  padding: 14px;
  border: 1px solid var(--wx-line);
  border-radius: 10px;
  background: #f7faf7;
}

.project-choice label {
  font-weight: 720;
}

.project-choice small {
  color: var(--wx-muted);
  line-height: 1.5;
}

.task-link {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  padding: 13px;
  border: 1px solid var(--wx-line);
  border-radius: 10px;
  transition: border-color 160ms ease, transform 160ms ease;
}

.task-link:hover,
.task-link:focus-visible {
  border-color: var(--wx-green);
  outline: none;
  transform: translateY(-1px);
}

.task-link > span:nth-child(2) {
  display: grid;
  min-width: 0;
  gap: 4px;
}

.task-link strong,
.task-link small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-link small {
  color: var(--wx-muted);
}

.task-link i {
  color: var(--wx-green);
  font-size: 11px;
  font-style: normal;
}

.task-number {
  color: var(--wx-green);
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 12px;
}

@media (max-width: 980px) {
  .canvas-title {
    top: 18px;
    left: 18px;
  }

  .canvas-toolbar {
    top: auto;
    right: 14px;
    bottom: 46px;
    left: 14px;
    overflow-x: auto;
  }

  .canvas-toolbar :deep(.el-button) {
    flex: none;
  }

  .sse-state {
    flex: none;
  }

  .canvas-readout {
    bottom: 12px;
    left: 16px;
  }
}

@media (max-width: 560px) {
  .canvas-title p {
    display: none;
  }

  .toolbar-label {
    display: none;
  }

  .canvas-toolbar :deep(.el-button) {
    width: 38px;
    padding-inline: 0;
  }

  .sse-state {
    padding-inline: 5px;
    font-size: 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .task-link {
    transition: none;
  }
}
</style>
