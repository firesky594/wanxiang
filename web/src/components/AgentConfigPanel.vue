<template>
  <section v-if="agent" class="agent-config-panel" :aria-label="`${agent.name} 配置信息`">
    <header class="agent-config-identity">
      <div>
        <span class="identity-kicker">AGENT CONFIGURATION</span>
        <strong>{{ agent.name }}</strong>
      </div>
      <span
        class="identity-status"
        :class="{ connected: isAgentConnected(agent.status) }"
      >
        <i></i>
        {{ agent.status || '状态未知' }}
      </span>
    </header>

    <dl class="agent-config-readout">
      <div>
        <dt>Provider</dt>
        <dd>{{ agent.provider_type || '未配置' }}</dd>
      </div>
      <div>
        <dt>密钥状态</dt>
        <dd>{{ agent.secret_configured ? '已配置' : '缺少密钥' }}</dd>
      </div>
      <div>
        <dt>当前模型</dt>
        <dd>{{ agent.model || '未选择模型' }}</dd>
      </div>
      <div>
        <dt>连接地址</dt>
        <dd>{{ agent.base_url || '使用默认地址' }}</dd>
      </div>
    </dl>

    <el-alert
      v-if="agent.last_error"
      :title="agent.last_error"
      type="error"
      :closable="false"
      show-icon
    />

    <el-alert
      v-if="operationError"
      :title="operationError"
      type="error"
      :closable="false"
      show-icon
    />

    <el-form class="agent-config-form" label-position="top" @submit.prevent>
      <el-form-item label="Agent 名称">
        <el-input :model-value="agent.name" disabled />
      </el-form-item>

      <el-form-item label="接口类型">
        <el-select
          v-model="form.provider_type"
          class="full-width"
          @change="applyProviderDefault"
        >
          <el-option label="OpenAI" value="openai" />
          <el-option label="DeepSeek" value="deepseek" />
        </el-select>
      </el-form-item>

      <el-form-item label="模型名称">
        <el-input v-model="form.model" placeholder="输入模型名称" />
      </el-form-item>

      <el-form-item label="接口 Base URL">
        <el-input v-model="form.base_url" placeholder="输入兼容接口地址" />
      </el-form-item>

      <el-form-item label="API Key">
        <el-input
          v-model="form.api_key"
          type="password"
          show-password
          autocomplete="new-password"
          :placeholder="apiKeyPlaceholder"
        />
      </el-form-item>
    </el-form>

    <p class="agent-config-security">
      已保存的密钥不会回显；编辑已有 Agent 时留空可保留当前密钥。
    </p>

    <div class="agent-config-actions">
      <el-button
        type="primary"
        :loading="saving"
        :disabled="probing"
        @click="save"
      >
        保存并探测
      </el-button>
      <el-button :loading="probing" :disabled="saving" @click="probe">
        重新探测
      </el-button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import {
  probeAgent,
  saveAgentConfig,
  type AgentConfig,
  type ProviderType
} from '../api/client'

const providerDefaults: Record<ProviderType, string> = {
  openai: 'https://api.openai.com/v1',
  deepseek: 'https://api.deepseek.com'
}

const modelDefaults: Record<ProviderType, string> = {
  openai: 'gpt-5.2',
  deepseek: 'deepseek-v4-flash'
}

const props = defineProps<{
  agent: AgentConfig | null
}>()

const emit = defineEmits<{
  updated: []
}>()

const saving = ref(false)
const probing = ref(false)
const operationError = ref('')
const form = reactive({
  provider_type: 'openai' as ProviderType,
  model: modelDefaults.openai,
  base_url: providerDefaults.openai,
  api_key: ''
})

const apiKeyPlaceholder = computed(() =>
  props.agent?.secret_configured ? '留空以保留当前密钥' : '输入 API Key')

/** 判断 Agent 状态是否代表当前已建立连接。 */
function isAgentConnected(status: string) {
  return ['online', 'busy'].includes(status.trim().toLowerCase())
}

/** 使用指定 Agent 的脱敏配置初始化编辑表单。 */
function hydrateForm(agent: AgentConfig | null) {
  const provider = agent?.provider_type || 'openai'
  form.provider_type = provider
  form.model = agent?.model || modelDefaults[provider]
  form.base_url = agent?.base_url || providerDefaults[provider]
  form.api_key = ''
  operationError.value = ''
}

/** 按选择的 Provider 写入默认模型和接口地址。 */
function applyProviderDefault() {
  form.model = modelDefaults[form.provider_type]
  form.base_url = providerDefaults[form.provider_type]
}

/** 校验并保存当前 Agent 配置，同时执行真实接口探测。 */
async function save() {
  const agent = props.agent
  if (!agent || saving.value || probing.value) return
  if (!form.model.trim() || (!form.api_key.trim() && !agent.secret_configured)) {
    ElMessage.warning('请填写模型名称和 API Key')
    return
  }

  saving.value = true
  operationError.value = ''
  try {
    const saved = await saveAgentConfig(agent.name, {
      provider_type: form.provider_type,
      base_url: form.base_url.trim(),
      model: form.model.trim(),
      api_key: form.api_key.trim()
    })
    hydrateForm(saved)
    ElMessage.success('配置已保存，接口探测成功')
  } catch (error) {
    operationError.value = error instanceof Error ? error.message : '配置保存或接口探测失败'
    ElMessage.error('配置保存或接口探测失败，已刷新 Agent 状态')
  } finally {
    form.api_key = ''
    emit('updated')
    saving.value = false
  }
}

/** 重新探测当前 Agent 接口并清除密钥输入。 */
async function probe() {
  const agent = props.agent
  if (!agent || saving.value || probing.value) return

  probing.value = true
  operationError.value = ''
  try {
    const result = await probeAgent(agent.name)
    hydrateForm(result)
    ElMessage.success('接口探测成功')
  } catch (error) {
    operationError.value = error instanceof Error ? error.message : '接口探测失败'
    ElMessage.error('接口探测失败，已刷新 Agent 状态')
  } finally {
    form.api_key = ''
    emit('updated')
    probing.value = false
  }
}

watch(() => props.agent?.name, () => hydrateForm(props.agent), { immediate: true })
</script>

<style scoped>
.agent-config-panel {
  display: grid;
  gap: 18px;
}

.agent-config-identity {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 18px;
  border: 1px solid rgba(51, 126, 106, 0.24);
  border-radius: 14px;
  color: #eafff8;
  background:
    radial-gradient(circle at 88% 18%, rgba(90, 224, 187, 0.18), transparent 34%),
    linear-gradient(145deg, #183c32, #0b1c18);
  box-shadow: inset 0 1px rgba(255, 255, 255, 0.08);
}

.agent-config-identity > div {
  display: grid;
  min-width: 0;
  gap: 5px;
}

.identity-kicker {
  color: rgba(165, 233, 214, 0.56);
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 9px;
  letter-spacing: 0.14em;
}

.agent-config-identity strong {
  overflow: hidden;
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.identity-status {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  flex: none;
  color: #ff9a8e;
  font-size: 11px;
}

.identity-status i {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #e05248;
  box-shadow: 0 0 12px rgba(224, 82, 72, 0.65);
}

.identity-status.connected {
  color: #76e2c2;
}

.identity-status.connected i {
  background: #3fe09f;
  box-shadow: 0 0 12px rgba(63, 224, 159, 0.72);
}

.agent-config-readout {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1px;
  overflow: hidden;
  margin: 0;
  border: 1px solid var(--wx-line);
  border-radius: 12px;
  background: var(--wx-line);
}

.agent-config-readout > div {
  min-width: 0;
  padding: 12px;
  background: #f8fbf9;
}

.agent-config-readout dt {
  margin-bottom: 5px;
  color: var(--wx-muted);
  font-size: 10px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

.agent-config-readout dd {
  overflow: hidden;
  margin: 0;
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-config-form {
  display: grid;
  gap: 1px;
}

.agent-config-form :deep(.el-form-item) {
  margin-bottom: 12px;
}

.agent-config-security {
  margin: -4px 0 0;
  padding: 10px 12px;
  border-left: 3px solid var(--wx-green);
  color: var(--wx-muted);
  background: #f5f9f7;
  font-size: 12px;
  line-height: 1.6;
}

.agent-config-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.agent-config-actions :deep(.el-button) {
  min-width: 126px;
  margin-left: 0;
}

@media (max-width: 420px) {
  .agent-config-readout {
    grid-template-columns: 1fr;
  }

  .agent-config-actions :deep(.el-button) {
    flex: 1;
  }
}
</style>
