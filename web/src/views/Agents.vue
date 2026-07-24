<template>
  <section class="console">
    <main class="main">
      <div class="page-head">
        <div>
          <h1>Agent 模型配置</h1>
          <p>每个 Agent 独立选择接口类型、模型、地址和密钥。保存后后端会执行一次真实接口探测。</p>
        </div>
        <el-button :loading="loading" @click="load">刷新</el-button>
      </div>

      <section class="grid two">
        <div class="panel stack">
          <div v-for="agent in agents" :key="agent.name" class="agent-row" @click="edit(agent)">
            <div>
              <strong>{{ agent.name }}</strong>
              <div class="muted mono">{{ agent.provider_type || '未配置' }} · {{ agent.model || '未选择模型' }}</div>
            </div>
            <div class="inline">
              <el-tag :type="agent.status === 'online' ? 'success' : 'warning'">{{ agent.status }}</el-tag>
              <el-tag :type="agent.secret_configured ? 'success' : 'danger'" effect="plain">
                {{ agent.secret_configured ? '密钥已配置' : '缺少密钥' }}
              </el-tag>
            </div>
          </div>
          <el-empty v-if="!loading && agents.length === 0" description="还没有 Agent 配置" />
        </div>

        <div class="panel stack">
          <h2>{{ form.name ? `配置 ${form.name}` : '新增 Agent' }}</h2>
          <el-input v-model="form.name" placeholder="Agent 名称，如 manager 或 backend-dev" :disabled="editingExisting" />
          <el-select v-model="form.provider_type" class="full-width" @change="applyProviderDefault">
            <el-option label="OpenAI" value="openai" />
            <el-option label="DeepSeek" value="deepseek" />
          </el-select>
          <el-input v-model="form.model" placeholder="模型名称" />
          <el-input v-model="form.base_url" placeholder="接口 Base URL" />
          <el-input v-model="form.api_key" type="password" show-password :placeholder="apiKeyPlaceholder" />
          <div class="muted">编辑已有 Agent 时留空密钥，将保留当前密钥。</div>
          <el-alert v-if="selected?.last_error" :title="selected.last_error" type="error" :closable="false" />
          <div class="inline">
            <el-button type="primary" :loading="saving" @click="save">保存并探测</el-button>
            <el-button :disabled="!editingExisting" :loading="probing" @click="probe">重新探测</el-button>
            <el-button @click="resetForm">新建</el-button>
          </div>
        </div>
      </section>
    </main>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { listAgentConfigs, probeAgent, saveAgentConfig, type AgentConfig, type ProviderType } from '../api/client'

const defaults: Record<ProviderType, string> = {
  openai: 'https://api.openai.com/v1',
  deepseek: 'https://api.deepseek.com'
}
const modelDefaults: Record<ProviderType, string> = {
  openai: 'gpt-5.2',
  deepseek: 'deepseek-v4-flash'
}

const agents = ref<AgentConfig[]>([])
const selected = ref<AgentConfig | null>(null)
const loading = ref(false)
const saving = ref(false)
const probing = ref(false)
const form = reactive({ name: '', provider_type: 'openai' as ProviderType, model: modelDefaults.openai, base_url: defaults.openai, api_key: '' })
const editingExisting = computed(() => agents.value.some((agent) => agent.name === form.name))
const apiKeyPlaceholder = computed(() => selected.value?.secret_configured ? '留空以保留当前密钥' : 'API Key')

onMounted(load)

async function load() {
  loading.value = true
  try {
    agents.value = await listAgentConfigs()
  } finally {
    loading.value = false
  }
}

function edit(agent: AgentConfig) {
  selected.value = agent
  form.name = agent.name
  form.provider_type = agent.provider_type || 'openai'
  form.model = agent.model || modelDefaults[form.provider_type]
  form.base_url = agent.base_url || defaults[form.provider_type]
  form.api_key = ''
}

function applyProviderDefault() {
  form.base_url = defaults[form.provider_type]
  form.model = modelDefaults[form.provider_type]
}

function resetForm() {
  selected.value = null
  Object.assign(form, { name: '', provider_type: 'openai', model: modelDefaults.openai, base_url: defaults.openai, api_key: '' })
}

defineExpose({ form, edit, applyProviderDefault, resetForm })

async function save() {
  if (!form.name.trim() || !form.model.trim() || (!form.api_key.trim() && !selected.value?.secret_configured)) {
    ElMessage.warning('请填写 Agent 名称、模型和 API Key')
    return
  }
  saving.value = true
  try {
    const saved = await saveAgentConfig(form.name.trim(), {
      provider_type: form.provider_type,
      base_url: form.base_url.trim(),
      model: form.model.trim(),
      api_key: form.api_key.trim()
    })
    ElMessage.success('配置已保存，接口探测成功')
    await load()
    edit(saved)
  } finally {
    saving.value = false
  }
}

async function probe() {
  probing.value = true
  try {
    const result = await probeAgent(form.name)
    ElMessage.success('接口探测成功')
    await load()
    edit(result)
  } finally {
    probing.value = false
  }
}
</script>

<style scoped>
.agent-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px;
  border: 1px solid var(--wx-line);
  border-radius: 8px;
  cursor: pointer;
}

.agent-row:hover {
  border-color: var(--wx-green);
  background: #f7faf7;
}
</style>
