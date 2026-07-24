// @vitest-environment jsdom
import { shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Agents from '../../src/views/Agents.vue'

vi.mock('../../src/api/client', () => ({
  listAgentConfigs: vi.fn().mockResolvedValue([]),
  probeAgent: vi.fn(),
  saveAgentConfig: vi.fn()
}))

type AgentsView = {
  form: { name: string; provider_type: 'openai' | 'deepseek'; model: string; base_url: string; api_key: string }
  edit: (agent: Record<string, unknown>) => void
  applyProviderDefault: () => void
  resetForm: () => void
}

function mountView() {
  return shallowMount(Agents, {
    global: {
      stubs: {
        RouterLink: true,
        'el-alert': true,
        'el-button': true,
        'el-empty': true,
        'el-icon': true,
        'el-input': true,
        'el-option': true,
        'el-select': true,
        'el-tag': true
      }
    }
  })
}

describe('agent model defaults', () => {
  beforeEach(() => vi.clearAllMocks())

  it('starts and resets new agents with the OpenAI default', () => {
    const view = mountView().vm as unknown as AgentsView
    expect(view.form.model).toBe('gpt-5.2')
    view.form.model = 'custom-model'
    view.resetForm()
    expect(view.form.provider_type).toBe('openai')
    expect(view.form.model).toBe('gpt-5.2')
  })

  it('applies the DeepSeek default when the provider changes', () => {
    const view = mountView().vm as unknown as AgentsView
    view.form.provider_type = 'deepseek'
    view.applyProviderDefault()
    expect(view.form.model).toBe('deepseek-v4-flash')
    expect(view.form.base_url).toBe('https://api.deepseek.com')
  })

  it('defaults an empty persisted model but preserves a non-empty one', () => {
    const view = mountView().vm as unknown as AgentsView
    view.edit({ name: 'empty', provider_type: 'deepseek', model: '', base_url: '', secret_configured: true })
    expect(view.form.model).toBe('deepseek-v4-flash')
    view.edit({ name: 'custom', provider_type: 'openai', model: 'persisted-model', base_url: '', secret_configured: true })
    expect(view.form.model).toBe('persisted-model')
  })
})
