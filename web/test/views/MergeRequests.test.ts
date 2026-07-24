import { describe, expect, it } from 'vitest'

const files = import.meta.glob(['/src/views/MergeRequests.vue', '/src/api/client.ts'], {
  eager: true,
  import: 'default',
  query: '?raw'
}) as Record<string, string>

describe('merge request read-only console', () => {
  it('uses administrator read APIs and exposes no code lifecycle writes', () => {
    const view = files['/src/views/MergeRequests.vue']
    const client = files['/src/api/client.ts']

    expect(view).toContain('listMergeRequests')
    expect(view).toContain('getMergeRequest')
    expect(client).toContain('/api/admin/mrs')
    expect(view).not.toContain('/api/agent/')
    expect(view).not.toContain('created_by')
    expect(view).not.toContain('createMR')
    expect(view).not.toContain('mergeMR')
  })

  it('shows report evidence, review history, loading and empty states', () => {
    const view = files['/src/views/MergeRequests.vue']

    for (const label of ['报告版本', '测试证据', '风险', '审核记录', '暂无合并请求', '加载失败']) {
      expect(view).toContain(label)
    }
    expect(view).toContain('aria-live="polite"')
    expect(view).toContain('@media (max-width: 880px)')
    expect(view).toContain('prefers-reduced-motion')
  })
})
