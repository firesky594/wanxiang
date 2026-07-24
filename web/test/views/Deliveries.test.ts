import { describe, expect, it } from 'vitest'

const files = import.meta.glob(['/src/views/Deliveries.vue', '/src/api/client.ts'], { eager: true, import: 'default', query: '?raw' }) as Record<string, string>

describe('delivery acceptance console', () => {
  it('shows immutable evidence, decisions and rework history', () => {
    const view = files['/src/views/Deliveries.vue']
    for (const label of ['交付验收', '测试证据', '风险与未完成项', '决定历史', '返工版本', '高风险操作需要单独确认', '暂无交付快照']) expect(view).toContain(label)
    expect(view).toContain('aria-live="polite"')
    expect(view).toContain('@media (max-width: 900px)')
    expect(view).toContain('prefers-reduced-motion')
  })

  it('uses administrator APIs and guards decision submission', () => {
    const view = files['/src/views/Deliveries.vue']
    const client = files['/src/api/client.ts']
    expect(client).toContain('/api/admin/deliveries')
    expect(view).toContain('submitDecision')
    expect(view).toContain('submitting')
    expect(view).toContain('拒绝或要求调整时必须填写意见')
		expect(view).toContain('decisionKey')
		expect(view).not.toContain('crypto.randomUUID()}`')
    expect(view).not.toContain('/api/agent/')
  })
})
