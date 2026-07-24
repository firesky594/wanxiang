<template>
  <VueFlow :nodes="nodes" :edges="edges" fit-view-on-init class="workflow-graph" />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { VueFlow } from '@vue-flow/core'
import '@vue-flow/core/dist/style.css'
import type { RuntimeEvent } from '../stores/events'

const props = defineProps<{ events: RuntimeEvent[] }>()

/** 将运行事件转换为工作流图节点与布局坐标。 */
const nodes = computed(() =>
  props.events.map((event, index) => ({
    id: String(event.id),
    position: { x: 80 + index * 220, y: index % 2 === 0 ? 80 : 220 },
    data: { label: `${event.actor}: ${event.type}` }
  }))
)

/** 按事件先后关系生成工作流图连接边。 */
const edges = computed(() =>
  props.events.slice(1).map((event, index) => ({
    id: `e-${index}`,
    source: String(props.events[index].id),
    target: String(event.id),
    label: event.type
  }))
)
</script>

<style scoped>
.workflow-graph {
  height: 520px;
  border: 1px solid #d7ddd4;
  border-radius: 8px;
  background: #fbfcfa;
}
</style>
