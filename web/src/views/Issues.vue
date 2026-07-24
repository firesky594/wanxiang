<template>
  <section class="console">
    <main class="main">
      <div class="page-head">
        <div>
          <h1>人工 Issue</h1>
          <p>当人工判断和总管不同，以人工 issue 为准；阻塞 issue 会阻止 MR 进入 main。</p>
        </div>
      </div>
      <section class="grid two">
        <div class="panel stack">
          <h2>创建 Issue</h2>
          <el-input-number v-model="form.task_id" class="full-width" :min="0" placeholder="task_id" />
          <el-input-number v-model="form.mr_id" class="full-width" :min="0" placeholder="mr_id" />
          <el-input v-model="form.title" placeholder="标题" />
          <el-input v-model="form.body" type="textarea" :rows="5" placeholder="人工意见" />
          <el-checkbox v-model="form.blocking">阻塞</el-checkbox>
          <el-button type="danger" :loading="creating" @click="createIssue">
            <el-icon><Warning /></el-icon>
            提交
          </el-button>
        </div>
        <div class="panel">
          <el-alert v-if="created" type="warning" :closable="false">
            Issue #{{ created.id }}：{{ created.status }}
          </el-alert>
          <el-empty v-else description="尚未创建 issue" />
        </div>
      </section>
    </main>
  </section>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { Warning } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { api, type Issue } from '../api/client'

const form = reactive({
  task_id: 0,
  mr_id: 0,
  title: '',
  body: '',
  blocking: true,
  created_by: 'admin'
})
const created = ref<Issue | null>(null)
const creating = ref(false)

async function createIssue() {
  creating.value = true
  try {
    const payload = {
      ...form,
      task_id: form.task_id || undefined,
      mr_id: form.mr_id || undefined
    }
    const res = await api<{ ok: boolean; issue: Issue }>('/api/admin/issues', {
      method: 'POST',
      body: JSON.stringify(payload)
    })
    created.value = res.issue
    ElMessage.success('人工 issue 已创建')
  } finally {
    creating.value = false
  }
}
</script>
