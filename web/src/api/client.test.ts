import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { api } from './client'
import { useAuthStore } from '../stores/auth'

describe('authenticated API client', () => {
  const setItem = vi.fn()

  beforeEach(() => {
    vi.restoreAllMocks()
    setItem.mockReset()
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => 'saved-admin-token'),
      setItem
    })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' }
    })))
  })

  it('sends the saved bearer token and same-origin credentials', async () => {
    await api('/api/admin/manager/status')

    const [, init] = vi.mocked(fetch).mock.calls[0]
    const headers = new Headers(init?.headers)
    expect(headers.get('Authorization')).toBe('Bearer saved-admin-token')
    expect(init?.credentials).toBe('same-origin')
  })

  it('bootstraps the first admin and saves its session token', async () => {
    setActivePinia(createPinia())
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ ok: true, token: 'bootstrap-token' }), {
      status: 201,
      headers: { 'Content-Type': 'application/json' }
    }))
    const store = useAuthStore()

    await store.bootstrap('admin', 'secret123')

    expect(store.token).toBe('bootstrap-token')
    expect(setItem).toHaveBeenCalledWith('wanxiang_admin_token', 'bootstrap-token')
    const [, init] = vi.mocked(fetch).mock.calls[0]
    expect(init?.method).toBe('POST')
    expect(init?.body).toBe(JSON.stringify({ username: 'admin', password: 'secret123' }))
  })
})
