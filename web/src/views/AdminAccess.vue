<template>
  <main class="access-shell">
    <div class="access-brand">
      <span class="brand-mark"><el-icon><Cpu /></el-icon></span>
      <strong>Wanxiang Agent</strong>
    </div>
    <section class="panel access-panel stack">
      <div>
        <h1>管理员访问</h1>
        <p class="muted">{{ mode === 'login' ? '登录控制台' : '创建首位管理员' }}</p>
      </div>
      <el-segmented v-model="mode" :options="modes" class="full-width" />
      <el-form label-position="top" @submit.prevent="submit">
        <el-form-item label="用户名">
          <el-input v-model="username" autocomplete="username" :prefix-icon="User" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="password"
            type="password"
            show-password
            autocomplete="current-password"
            :prefix-icon="Lock"
          />
        </el-form-item>
        <el-button class="full-width" type="primary" native-type="submit" :loading="submitting">
          {{ mode === 'login' ? '登录' : '初始化管理员' }}
        </el-button>
      </el-form>
    </section>
  </main>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Cpu, Lock, User } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '../stores/auth'

type AccessMode = 'login' | 'bootstrap'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const mode = ref<AccessMode>(route.path === '/bootstrap' ? 'bootstrap' : 'login')
const modes = [
  { label: '登录', value: 'login' },
  { label: '首次初始化', value: 'bootstrap' }
]
const username = ref('admin')
const password = ref('')
const submitting = ref(false)

async function submit() {
  if (!username.value.trim() || !password.value) {
    ElMessage.warning('请输入用户名和密码')
    return
  }
  submitting.value = true
  try {
    if (mode.value === 'bootstrap') {
      await auth.bootstrap(username.value.trim(), password.value)
    } else {
      await auth.login(username.value.trim(), password.value)
    }
    const redirect = typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/')
      ? route.query.redirect
      : '/dashboard'
    await router.replace(redirect)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '认证失败')
  } finally {
    submitting.value = false
  }
}
</script>
