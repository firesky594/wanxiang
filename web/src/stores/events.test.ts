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
})
