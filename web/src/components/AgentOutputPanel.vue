<template>
  <section class="panel stack">
    <div class="inline">
      <el-icon><Connection /></el-icon>
      <h3>Agent 输出</h3>
    </div>
    <el-empty v-if="events.length === 0" description="等待实时事件" />
    <el-timeline v-else>
      <el-timeline-item
        v-for="event in recent"
        :key="event.id"
        :timestamp="new Date(event.created_at).toLocaleString()"
      >
        <div class="stack">
          <div class="inline">
            <el-tag size="small">{{ event.actor }}</el-tag>
            <span class="mono">{{ event.type }}</span>
          </div>
          <pre>{{ formatPayload(event.payload) }}</pre>
        </div>
      </el-timeline-item>
    </el-timeline>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Connection } from '@element-plus/icons-vue'
import type { RuntimeEvent } from '../stores/events'

const props = defineProps<{ events: RuntimeEvent[] }>()

const recent = computed(() => props.events.slice(-8).reverse())

function formatPayload(payload: unknown) {
  return JSON.stringify(payload ?? {}, null, 2)
}
</script>

<style scoped>
pre {
  max-height: 190px;
  margin: 0;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  color: #2d3837;
  background: #f3f6f2;
  border: 1px solid #d7ddd4;
  border-radius: 8px;
  padding: 10px;
}
</style>
