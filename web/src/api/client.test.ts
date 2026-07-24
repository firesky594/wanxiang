// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { api, cleanupTaskWorkspace, createAdminTask, extendLeaseDeadline, freezeLease, overrideTaskMatch, reassignLease, repairTaskWorkspace, saveAgentConfig, unfreezeLease } from './client'
import { useAuthStore } from '../stores/auth'

describe('authenticated API client', () => {
  const setItem = vi.fn()
  const removeItem = vi.fn()

  beforeEach(() => {
    vi.restoreAllMocks()
    setItem.mockReset()
    removeItem.mockReset()
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => 'saved-admin-token'),
      setItem,
      removeItem
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

  it('clears stale login state and announces reauthentication on 401', async () => {
    const unauthorized = vi.fn()
    window.addEventListener('wanxiang:admin-unauthorized', unauthorized, { once: true })
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      ok: false,
      error: 'invalid or expired admin session'
    }), { status: 401, headers: { 'Content-Type': 'application/json' } }))

    await expect(api('/api/admin/tasks?limit=100&offset=0')).rejects.toThrow('invalid or expired admin session')

    expect(removeItem).toHaveBeenCalledWith('wanxiang_admin_token')
    expect(removeItem).toHaveBeenCalledWith('wanxiang_workspace_v2')
    expect(unauthorized).toHaveBeenCalledOnce()
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

  it('saves an agent provider configuration without putting the name in the body', async () => {
    await saveAgentConfig('worker 1', {
      provider_type: 'deepseek',
      base_url: 'https://api.deepseek.com',
      model: 'deepseek-test',
      api_key: 'replacement-key'
    })

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/admin/agents/worker%201/config')
    expect(init?.method).toBe('PUT')
    expect(JSON.parse(String(init?.body))).toEqual({
      provider_type: 'deepseek',
      base_url: 'https://api.deepseek.com',
      model: 'deepseek-test',
      api_key: 'replacement-key'
    })
  })

  it('sends an explicit administrator assignment override', async () => {
	await overrideTaskMatch(12, 34, 'worker-a')

	const [url, init] = vi.mocked(fetch).mock.calls[0]
	expect(url).toBe('/api/admin/tasks/12/match')
	expect(init?.method).toBe('PUT')
	expect(JSON.parse(String(init?.body))).toEqual({ step_id: 34, agent_name: 'worker-a' })
  })

  it('reuses a project only by registered project id', async () => {
    await createAdminTask('follow-up', 'same repository', 42)
    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/admin/tasks')
    expect(JSON.parse(String(init?.body))).toEqual({ title: 'follow-up', description: 'same repository', project_id: 42 })
  })

  it('uses explicit workspace repair and cleanup requests', async () => {
    await repairTaskWorkspace(12, 'git_snapshot')
    await cleanupTaskWorkspace(12, 'request', true)
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/admin/tasks/12/workspace/repair')
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[0][1]?.body))).toEqual({ direction: 'git_snapshot' })
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[1][1]?.body))).toEqual({ action: 'request', confirmed: true })
  })

  it('uses explicit lease recovery administrator requests', async () => {
    await extendLeaseDeadline(12, 34, 'lease-1', 2, '2026-07-15T12:00:00Z')
    await freezeLease(12, 34, 'manual review')
    await unfreezeLease(12, 34)
    await reassignLease(12, 34, 'worker-b', { checkpoint_id: 56, immediate: true, reason: 'timeout' })

    const calls = vi.mocked(fetch).mock.calls
    expect(calls[0][0]).toBe('/api/admin/tasks/12/steps/34/lease/extend')
    expect(JSON.parse(String(calls[0][1]?.body))).toEqual({ lease_id: 'lease-1', lease_version: 2, resume_deadline: '2026-07-15T12:00:00Z' })
    expect(calls[1][0]).toBe('/api/admin/tasks/12/steps/34/lease/freeze')
    expect(calls[2][0]).toBe('/api/admin/tasks/12/steps/34/lease/unfreeze')
    expect(JSON.parse(String(calls[3][1]?.body))).toEqual({ new_agent: 'worker-b', checkpoint_id: 56, immediate: true, reason: 'timeout' })
  })
})
