import { defineStore } from 'pinia'

export interface RuntimeEvent {
  id: number
  task_id?: number
  type: string
  actor: string
  payload: unknown
  created_at: string
}

export const useEventsStore = defineStore('events', {
  /** 初始化事件列表、连接状态和 SSE 事件源。 */
  state: () => ({
    events: [] as RuntimeEvent[],
    connected: false,
    source: null as EventSource | null
  }),
  actions: {
    /** 合并历史事件、过滤心跳并按编号排序。 */
    hydrate(items: RuntimeEvent[]) {
      const merged = new Map(this.events.map((event) => [event.id, event]))
      items.forEach((event) => {
        if (event.type !== 'agent.heartbeat') merged.set(event.id, event)
      })
      this.events = [...merged.values()].sort((a, b) => a.id - b.id)
    },
    /** 建立 SSE 连接，并通过通用消息通道接收全部运行事件。 */
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
    },
    /** 解析、过滤、去重并追加一条实时事件。 */
    pushEvent(msg: MessageEvent) {
      const event = JSON.parse(msg.data) as RuntimeEvent
      if (event.type === 'agent.heartbeat') {
        return
      }
      if (!this.events.some((item) => item.id === event.id)) {
        this.events.push(event)
        this.events.sort((a, b) => a.id - b.id)
      }
    }
  }
})
