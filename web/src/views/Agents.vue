<template>
  <section class="console">
    <header class="topbar">
      <RouterLink class="brand" to="/dashboard">
        <span class="brand-mark"><el-icon><Cpu /></el-icon></span>
        <span>Wanxiang Agent</span>
      </RouterLink>
      <nav class="nav">
        <RouterLink to="/dashboard"><el-icon><ArrowRight /></el-icon>调度台</RouterLink>
        <RouterLink to="/init-manager"><el-icon><Key /></el-icon>总管</RouterLink>
      </nav>
    </header>
    <main class="main">
      <div class="page-head">
        <div>
          <h1>Agents</h1>
          <p>本地 agent 心跳会写入注册表，后续列表查询 API 完成后这里展示完整队列。</p>
        </div>
        <el-button @click="loadManager">刷新总管</el-button>
      </div>
      <section class="panel">
        <el-descriptions :column="1" border>
          <el-descriptions-item label="name">manager</el-descriptions-item>
          <el-descriptions-item label="status">{{ manager?.status || '-' }}</el-descriptions-item>
          <el-descriptions-item label="missing env">
            <el-tag v-for="key in manager?.missing_env || []" :key="key" type="danger" effect="plain">
              {{ key }}
            </el-tag>
          </el-descriptions-item>
        </el-descriptions>
      </section>
    </main>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ArrowRight, Cpu, Key } from '@element-plus/icons-vue'
import type { ManagerStatus } from '../api/client'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const manager = ref<ManagerStatus | null>(null)

onMounted(loadManager)

async function loadManager() {
  manager.value = await auth.loadManager()
}
</script>
