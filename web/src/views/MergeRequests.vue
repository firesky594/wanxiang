<template>
  <section class="console">
    <header class="topbar">
      <RouterLink class="brand" to="/dashboard">
        <span class="brand-mark"><el-icon><Cpu /></el-icon></span>
        <span>Wanxiang Agent</span>
      </RouterLink>
      <nav class="nav">
        <RouterLink to="/dashboard"><el-icon><ArrowRight /></el-icon>调度台</RouterLink>
        <RouterLink to="/issues"><el-icon><Warning /></el-icon>Issue</RouterLink>
      </nav>
    </header>
    <main class="main">
      <div class="page-head">
        <div>
          <h1>本地 MR</h1>
          <p>agent 可以创建本地 MR；合并 main 只能由 manager actor 发起。</p>
        </div>
      </div>
      <section class="grid two">
        <div class="panel stack">
          <h2>创建 MR</h2>
          <el-input-number v-model="form.project_id" class="full-width" :min="1" placeholder="project_id" />
          <el-input-number v-model="form.task_id" class="full-width" :min="1" placeholder="task_id" />
          <el-input v-model="form.title" placeholder="标题" />
          <el-input v-model="form.source_branch" placeholder="agent/backend/task-1" />
          <el-input v-model="form.created_by" placeholder="backend-dev" />
          <el-button type="primary" :loading="creating" @click="createMR">
            <el-icon><Share /></el-icon>
            创建
          </el-button>
        </div>
        <div class="panel stack">
          <h2>Manager 合并</h2>
          <el-input-number v-model="mergeID" class="full-width" :min="1" placeholder="mr_id" />
          <el-button type="warning" :loading="merging" @click="mergeMR">请求合并 main</el-button>
          <el-alert v-if="created" type="success" :closable="false">
            MR #{{ created.id }} {{ created.source_branch }} → {{ created.target_branch }}
          </el-alert>
        </div>
      </section>
    </main>
  </section>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ArrowRight, Cpu, Share, Warning } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { api, type MergeRequest } from '../api/client'

const form = reactive({
  project_id: 1,
  task_id: 1,
  title: '',
  source_branch: '',
  created_by: ''
})
const created = ref<MergeRequest | null>(null)
const mergeID = ref(1)
const creating = ref(false)
const merging = ref(false)

async function createMR() {
  creating.value = true
  try {
    const res = await api<{ ok: boolean; merge_request: MergeRequest }>('/api/agent/mr/create', {
      method: 'POST',
      body: JSON.stringify(form)
    })
    created.value = res.merge_request
    mergeID.value = res.merge_request.id
    ElMessage.success('MR 已创建')
  } finally {
    creating.value = false
  }
}

async function mergeMR() {
  merging.value = true
  try {
    await api<{ ok: boolean }>(`/api/agent/mr/${mergeID.value}/merge`, {
      method: 'POST',
      body: JSON.stringify({ actor: 'manager' })
    })
    ElMessage.success('已由 manager 合并')
  } finally {
    merging.value = false
  }
}
</script>
