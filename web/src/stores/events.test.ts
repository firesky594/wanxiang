import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useEventsStore } from './events'

describe('runtime event store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('keeps heartbeat events out of the visible event stream', () => {
    const store = useEventsStore()

    store.pushEvent({
      data: JSON.stringify({ id: 1, type: 'agent.heartbeat', actor: 'backend-dev' })
    } as MessageEvent)
    store.pushEvent({
      data: JSON.stringify({ id: 2, type: 'task.created', actor: 'admin' })
    } as MessageEvent)

    expect(store.events).toHaveLength(1)
    expect(store.events[0].type).toBe('task.created')
  })

  it('hydrates persisted events without duplicating SSE events', () => {
    const store = useEventsStore()
    store.hydrate([{ id: 2, task_id: 1, type: 'task.created', actor: 'admin', payload: {}, created_at: '2026-07-14T00:00:00Z' }])
    store.pushEvent({ data: JSON.stringify({ id: 2, task_id: 1, type: 'task.created', actor: 'admin' }) } as MessageEvent)
    store.pushEvent({ data: JSON.stringify({ id: 3, task_id: 1, type: 'mr.created', actor: 'agent' }) } as MessageEvent)
    expect(store.events.map((event) => event.id)).toEqual([2, 3])
  })
})
