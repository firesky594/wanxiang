<template>
  <section class="console">
    <header class="topbar">
      <RouterLink class="brand" to="/dashboard">
        <span class="brand-mark"><el-icon><Cpu /></el-icon></span>
        <span>Wanxiang Agent</span>
      </RouterLink>
      <nav class="nav">
        <RouterLink to="/dashboard"><el-icon><ArrowRight /></el-icon>调度台</RouterLink>
      </nav>
    </header>
    <main class="main">
      <div class="page-head">
        <div>
          <h1>总管初始化</h1>
          <p>Go 启动时会创建总管模板；缺少密钥时总管保持阻塞，等待人工确认。</p>
        </div>
      </div>
      <section class="grid two">
        <div class="panel stack">
          <h2>状态</h2>
          <el-skeleton v-if="loading" :rows="4" animated />
          <template v-else>
            <el-tag :type="manager?.status === 'online' ? 'success' : 'warning'">
              {{ manager?.status || '-' }}
            </el-tag>
            <div class="muted">缺少环境变量</div>
            <el-space wrap>
              <el-tag v-for="key in manager?.missing_env || []" :key="key" type="danger" effect="plain">
                {{ key }}
              </el-tag>
            </el-space>
          </template>
        </div>
        <div class="panel stack">
          <h2>写入本地 env</h2>
          <el-input v-model="key" placeholder="MANAGER_API_KEY" />
          <el-input v-model="value" type="password" show-password placeholder="API Key" />
          <el-button type="primary" :loading="saving" @click="save">
            <el-icon><Key /></el-icon>
            保存并重新检查
          </el-button>
        </div>
      </section>
    </main>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ArrowRight, Cpu, Key } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import type { ManagerStatus } from '../api/client'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const manager = ref<ManagerStatus | null>(null)
const loading = ref(true)
const saving = ref(false)
const key = ref('MANAGER_API_KEY')
const value = ref('')

onMounted(load)

async function load() {
  loading.value = true
  try {
    manager.value = await auth.loadManager()
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!value.value.trim()) {
    ElMessage.warning('请输入 API Key')
    return
  }
  saving.value = true
  try {
    manager.value = await auth.saveManagerSecret(key.value, value.value)
    value.value = ''
    ElMessage.success('总管 env 已保存')
  } finally {
    saving.value = false
  }
}
</script>
