import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useTasksStore } from './tasks'

describe('persisted task store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.stubGlobal('localStorage', { getItem: vi.fn(() => 'token') })
  })

  it('loads task list and detail from admin APIs', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true, tasks: [{ id: 2, title: 'saved', project_id: 1, project_slug: 'p', description: '', status: 'created' }] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true, detail: { task: { id: 2, title: 'saved' }, project: { id: 1, slug: 'p' }, steps: [], edges: [] } }), { status: 200 })))
    const store = useTasksStore()
    await store.loadList()
    await store.loadDetail(2)
    expect(store.tasks[0].title).toBe('saved')
    expect(store.detail?.project.slug).toBe('p')
  })
})
