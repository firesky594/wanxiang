import { describe, expect, it } from 'vitest'

const files = import.meta.glob('./Agents.vue', { eager: true, import: 'default', query: '?raw' }) as Record<string, string>

describe('agent model defaults', () => {
  it('uses provider defaults without replacing persisted models', () => {
    const view = files['./Agents.vue']
    expect(view).toContain("openai: 'gpt-5.2'")
    expect(view).toContain("deepseek: 'deepseek-v4-flash'")
    expect(view).toContain('form.model = agent.model || modelDefaults[form.provider_type]')
    expect(view).toContain('form.model = modelDefaults[form.provider_type]')
  })
})
