<template>
  <section class="agent-canvas" aria-label="Agent 调度画布" data-testid="agent-canvas">
    <VueFlow
      :key="flowVersion"
      v-model:nodes="nodes"
      class="agent-flow"
      :min-zoom="0.35"
      :max-zoom="1.7"
      :nodes-connectable="false"
      :edges-updatable="false"
      :delete-key-code="null"
      @node-click="selectAgentFromNode"
      @node-drag-stop="persistNodePosition"
    >
      <template #node-agent="{ data, selected }">
        <article
          class="agent-pod"
          :class="[`tone-${data.tone}`, { selected, connected: data.connected }]"
          :data-agent-name="data.agent.name"
          role="button"
          tabindex="0"
          :aria-label="`查看 ${data.agent.name} 配置信息，${data.connected ? '已连接' : '未连接'}`"
          @keydown.enter.prevent="selectAgent(data.agent)"
          @keydown.space.prevent="selectAgent(data.agent)"
        >
          <header class="agent-pod__head">
            <strong :title="data.agent.name">{{ data.agent.name }}</strong>
            <span
              class="agent-connection"
              :class="{ connected: data.connected }"
              :aria-label="data.connected ? '已连接' : '未连接'"
              :title="data.connected ? '已连接' : '未连接'"
              role="status"
            ></span>
          </header>

          <div class="agent-portrait">
            <span class="agent-orbit orbit-one"></span>
            <span class="agent-orbit orbit-two"></span>
            <img
              :src="agentAvatar"
              :alt="`${data.agent.name} Agent 形象`"
              draggable="false"
            />
          </div>

          <footer class="agent-pod__foot">
            <span class="agent-status">{{ data.statusLabel }}</span>
            <small>{{ data.agent.model || data.agent.provider_type || '等待配置' }}</small>
          </footer>
        </article>
      </template>
    </VueFlow>

    <div v-if="loading" class="canvas-state" role="status">
      <span class="state-scanner"></span>
      正在同步 Agent
    </div>
    <div v-else-if="agents.length === 0" class="canvas-state">
      暂无可显示的 Agent
    </div>

    <div class="canvas-tip" aria-hidden="true">
      <span>拖动 Agent 调整位置</span>
      <span>拖动画布平移 · 滚轮缩放</span>
    </div>
  </section>
</template>

<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { VueFlow, type Node, type NodeDragEvent, type NodeMouseEvent } from '@vue-flow/core'
import '@vue-flow/core/dist/style.css'
import type { AgentConfig } from '../api/client'
import agentAvatar from '../assets/agent-avatar.svg'

const STORAGE_KEY = 'wanxiang_agent_canvas_v1'
const LAYOUT_VERSION = 1

interface SavedPosition {
  x: number
  y: number
}

interface LayoutSnapshot {
  version: number
  positions: Record<string, SavedPosition>
}

interface AgentNodeData {
  agent: AgentConfig
  connected: boolean
  statusLabel: string
  tone: string
}

const props = defineProps<{
  agents: AgentConfig[]
  loading?: boolean
}>()

const emit = defineEmits<{
  'select-agent': [agent: AgentConfig]
}>()

const nodes = ref<Node<AgentNodeData>[]>([])
const flowVersion = ref(0)

/** 向父级发送用户选择的 Agent。 */
function selectAgent(agent: AgentConfig) {
  emit('select-agent', agent)
}

/** 将 Vue Flow 节点点击转换为 Agent 选择事件。 */
function selectAgentFromNode({ node }: NodeMouseEvent) {
  const data = node.data as AgentNodeData
  if (data?.agent) selectAgent(data.agent)
}

/** 判断 Agent 原始状态是否代表当前可连接。 */
function isConnectedStatus(status: string) {
  const normalized = status.trim().toLowerCase()
  return normalized === 'online' || normalized === 'busy'
}

/** 将 Agent 原始状态转换为画布可读文字。 */
function formatAgentStatus(status: string) {
  const labels: Record<string, string> = {
    online: '在线',
    busy: '执行中',
    configured: '等待连接',
    probing: '连接中',
    'blocked: missing_config': '缺少配置',
    'blocked: provider_error': '接口异常',
    offline: '离线'
  }
  return labels[status.trim().toLowerCase()] || status || '状态未知'
}

/** 根据 Agent 名称稳定选择节点强调色。 */
function agentTone(name: string) {
  if (name === 'manager') return 'amber'
  const tones = ['mint', 'cyan', 'violet', 'coral']
  const hash = [...name].reduce((total, character) => total + character.charCodeAt(0), 0)
  return tones[hash % tones.length]
}

/** 读取并校验用户保存的 Agent 画布位置。 */
function readLayout(): LayoutSnapshot {
  if (typeof window === 'undefined') return { version: LAYOUT_VERSION, positions: {} }
  try {
    const parsed = JSON.parse(window.localStorage.getItem(STORAGE_KEY) || '{}') as Partial<LayoutSnapshot>
    if (parsed.version !== LAYOUT_VERSION || !parsed.positions) {
      return { version: LAYOUT_VERSION, positions: {} }
    }
    const positions = Object.fromEntries(
      Object.entries(parsed.positions).filter(([, position]) =>
        Number.isFinite(position?.x) && Number.isFinite(position?.y))
    )
    return { version: LAYOUT_VERSION, positions }
  } catch {
    return { version: LAYOUT_VERSION, positions: {} }
  }
}

/** 为尚未保存位置的 Agent 生成不重叠的初始坐标。 */
function defaultPosition(index: number) {
  const compact = typeof window !== 'undefined' && window.innerWidth <= 767
  if (compact) {
    return {
      x: Math.max(24, (window.innerWidth - 228) / 2),
      y: 150 + index * 320
    }
  }
  const column = index % 4
  const row = Math.floor(index / 4)
  return { x: 180 + column * 290, y: 190 + row * 290 }
}

/** 按最新 Agent 数据重建节点并保留用户拖动位置。 */
function syncAgentNodes(keepCurrentPosition = true) {
  const saved = readLayout().positions
  const current = new Map<string, SavedPosition>()
  nodes.value.forEach((node) => {
    if (node.data?.agent.name) current.set(node.data.agent.name, node.position)
  })
  nodes.value = props.agents.map((agent, index) => ({
    id: `agent:${agent.name}`,
    type: 'agent',
    ariaLabel: `${agent.name} Agent，${formatAgentStatus(agent.status)}，可拖动调整位置`,
    position: keepCurrentPosition
      ? current.get(agent.name) || saved[agent.name] || defaultPosition(index)
      : defaultPosition(index),
    data: {
      agent,
      connected: isConnectedStatus(agent.status),
      statusLabel: formatAgentStatus(agent.status),
      tone: agentTone(agent.name)
    },
    draggable: true,
    selectable: true,
    connectable: false
  }))
}

/** 在拖动结束后保存对应 Agent 的画布坐标。 */
function persistNodePosition({ node }: NodeDragEvent) {
  const data = node.data as AgentNodeData
  if (!data?.agent?.name) return
  const snapshot = readLayout()
  snapshot.positions[data.agent.name] = {
    x: node.position.x,
    y: node.position.y
  }
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(snapshot))
}

/** 清除全部自定义坐标并恢复默认布局。 */
async function resetLayout() {
  window.localStorage.removeItem(STORAGE_KEY)
  syncAgentNodes(false)
  flowVersion.value += 1
  await nextTick()
}

watch(() => props.agents, () => syncAgentNodes(), { deep: true, immediate: true })

defineExpose({ resetLayout })
</script>

<style scoped>
.agent-canvas,
.agent-flow {
  position: absolute;
  inset: 0;
}

.agent-canvas {
  overflow: hidden;
  background:
    radial-gradient(circle at 18% 16%, rgba(46, 211, 169, 0.14), transparent 30%),
    radial-gradient(circle at 82% 78%, rgba(84, 120, 255, 0.12), transparent 34%),
    #071310;
}

.agent-flow {
  background:
    linear-gradient(rgba(111, 202, 176, 0.055) 1px, transparent 1px),
    linear-gradient(90deg, rgba(111, 202, 176, 0.055) 1px, transparent 1px);
  background-size: 32px 32px;
}

.agent-flow::before {
  position: absolute;
  inset: 0;
  z-index: 0;
  background: repeating-linear-gradient(
    115deg,
    transparent 0,
    transparent 120px,
    rgba(145, 255, 224, 0.018) 121px,
    rgba(145, 255, 224, 0.018) 122px
  );
  content: "";
  pointer-events: none;
}

:deep(.vue-flow__pane) {
  cursor: grab;
}

:deep(.vue-flow__pane.dragging) {
  cursor: grabbing;
}

:deep(.vue-flow__node-agent) {
  width: 228px;
  border: 0;
  background: transparent;
  box-shadow: none;
}

.agent-pod {
  --agent-accent: #66dfbd;
  --agent-accent-soft: rgba(102, 223, 189, 0.22);
  position: relative;
  overflow: hidden;
  width: 228px;
  border: 1px solid color-mix(in srgb, var(--agent-accent) 48%, transparent);
  border-radius: 20px 20px 36px 36px;
  color: #eafff8;
  background:
    linear-gradient(145deg, rgba(28, 58, 50, 0.92), rgba(7, 20, 16, 0.96)),
    #0c1b17;
  box-shadow:
    0 24px 52px rgba(0, 0, 0, 0.34),
    inset 0 1px rgba(255, 255, 255, 0.08);
  transition: border-color 180ms ease, box-shadow 180ms ease, transform 180ms ease;
}

.agent-pod::after {
  position: absolute;
  inset: 0;
  border-radius: inherit;
  background: linear-gradient(120deg, transparent 25%, rgba(255, 255, 255, 0.06), transparent 62%);
  content: "";
  pointer-events: none;
  transform: translateX(-120%);
  transition: transform 360ms ease;
}

.agent-pod:hover::after,
.agent-pod.selected::after {
  transform: translateX(120%);
}

.agent-pod.selected {
  border-color: var(--agent-accent);
  box-shadow:
    0 0 0 3px var(--agent-accent-soft),
    0 28px 64px rgba(0, 0, 0, 0.42);
  transform: translateY(-3px);
}

.agent-pod:focus-visible {
  border-color: var(--agent-accent);
  outline: 3px solid var(--agent-accent-soft);
  outline-offset: 4px;
}

.tone-cyan {
  --agent-accent: #68d8ff;
  --agent-accent-soft: rgba(104, 216, 255, 0.2);
}

.tone-violet {
  --agent-accent: #b8a6ff;
  --agent-accent-soft: rgba(184, 166, 255, 0.2);
}

.tone-coral {
  --agent-accent: #ff9d83;
  --agent-accent-soft: rgba(255, 157, 131, 0.2);
}

.tone-amber {
  --agent-accent: #ffc96b;
  --agent-accent-soft: rgba(255, 201, 107, 0.22);
}

.agent-pod__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 48px;
  padding: 0 16px;
  border-bottom: 1px solid rgba(210, 255, 241, 0.1);
  background: rgba(255, 255, 255, 0.035);
}

.agent-pod__head strong {
  max-width: 174px;
  overflow: hidden;
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 13px;
  letter-spacing: 0.035em;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-connection {
  position: relative;
  width: 10px;
  height: 10px;
  flex: none;
  border: 2px solid rgba(255, 255, 255, 0.65);
  border-radius: 50%;
  background: #e05248;
  box-shadow: 0 0 14px rgba(224, 82, 72, 0.74);
}

.agent-connection.connected {
  background: #3fe09f;
  box-shadow: 0 0 16px rgba(63, 224, 159, 0.85);
}

.agent-connection.connected::after {
  position: absolute;
  inset: -6px;
  border: 1px solid rgba(63, 224, 159, 0.5);
  border-radius: 50%;
  content: "";
  animation: connection-pulse 1.8s ease-out infinite;
}

.agent-portrait {
  position: relative;
  display: grid;
  height: 176px;
  overflow: hidden;
  place-items: center;
  background:
    radial-gradient(circle, var(--agent-accent-soft), transparent 58%),
    linear-gradient(180deg, rgba(255, 255, 255, 0.018), transparent);
}

.agent-portrait img {
  position: relative;
  z-index: 2;
  width: 166px;
  height: 166px;
  object-fit: contain;
  pointer-events: none;
  user-select: none;
}

.agent-orbit {
  position: absolute;
  z-index: 1;
  border: 1px solid color-mix(in srgb, var(--agent-accent) 42%, transparent);
  border-radius: 50%;
}

.orbit-one {
  width: 146px;
  height: 146px;
  animation: orbit-spin 12s linear infinite;
}

.orbit-two {
  width: 110px;
  height: 166px;
  border-style: dashed;
  animation: orbit-spin 9s linear infinite reverse;
}

.agent-pod__foot {
  display: grid;
  gap: 4px;
  padding: 12px 16px 14px;
  border-top: 1px solid rgba(210, 255, 241, 0.1);
}

.agent-status {
  color: var(--agent-accent);
  font-size: 12px;
  font-weight: 760;
  letter-spacing: 0.08em;
}

.agent-pod__foot small {
  overflow: hidden;
  color: #8ea8a0;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.canvas-state {
  position: absolute;
  top: 50%;
  left: 50%;
  z-index: 10;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 18px;
  border: 1px solid rgba(107, 224, 191, 0.24);
  border-radius: 12px;
  color: #cce9df;
  background: rgba(8, 24, 19, 0.88);
  box-shadow: 0 16px 48px rgba(0, 0, 0, 0.28);
  transform: translate(-50%, -50%);
}

.state-scanner {
  width: 14px;
  height: 14px;
  border: 2px solid rgba(104, 224, 190, 0.24);
  border-top-color: #68e0be;
  border-radius: 50%;
  animation: orbit-spin 800ms linear infinite;
}

.canvas-tip {
  position: absolute;
  right: 22px;
  bottom: 18px;
  z-index: 6;
  display: flex;
  gap: 16px;
  color: rgba(205, 238, 228, 0.5);
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  letter-spacing: 0.05em;
  pointer-events: none;
}

@keyframes connection-pulse {
  from {
    opacity: 0.8;
    transform: scale(0.6);
  }
  to {
    opacity: 0;
    transform: scale(1.4);
  }
}

@keyframes orbit-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 767px) {
  .canvas-tip {
    right: 14px;
    bottom: 12px;
    flex-direction: column;
    gap: 3px;
    text-align: right;
  }
}

@media (prefers-reduced-motion: reduce) {
  .agent-connection.connected::after,
  .agent-orbit,
  .state-scanner {
    animation: none;
  }

  .agent-pod,
  .agent-pod::after {
    transition: none;
  }
}
</style>
