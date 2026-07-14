import { defineStore } from 'pinia'

export interface RuntimeEvent {
  id: number
  task_id?: number
  type: string
  actor: string
  payload: unknown
  created_at: string
}

const namedEvents = [
  'task.created',
  'mr.created',
  'mr.merged',
  'issue.created',
  'token.usage'
]

export const useEventsStore = defineStore('events', {
  state: () => ({
    events: [] as RuntimeEvent[],
    connected: false,
    source: null as EventSource | null
  }),
  actions: {
    connect() {
      if (this.source) {
        return
      }
      const source = new EventSource('/api/events/stream')
      this.source = source
      source.onopen = () => {
        this.connected = true
      }
      source.onerror = () => {
        this.connected = false
      }
      source.onmessage = (msg) => {
        this.pushEvent(msg)
      }
      namedEvents.forEach((name) => {
        source.addEventListener(name, (msg) => {
          this.pushEvent(msg as MessageEvent)
        })
      })
    },
    pushEvent(msg: MessageEvent) {
      const event = JSON.parse(msg.data) as RuntimeEvent
      if (event.type === 'agent.heartbeat') {
        return
      }
      if (!this.events.some((item) => item.id === event.id)) {
        this.events.push(event)
      }
    }
  }
})
