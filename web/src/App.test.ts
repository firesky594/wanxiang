import { describe, expect, it } from 'vitest'

const sourceFiles = import.meta.glob('./main.ts', {
  eager: true,
  import: 'default',
  query: '?raw'
}) as Record<string, string>

const appFiles = import.meta.glob([
  './App.vue',
  './views/Dashboard.vue',
  './views/Agents.vue',
  './views/TaskDetail.vue',
  './views/MergeRequests.vue',
  './views/Issues.vue',
  './views/Deliveries.vue',
  './views/Pipelines.vue'
], {
  eager: true,
  import: 'default',
  query: '?raw'
}) as Record<string, string>

describe('root application', () => {
  it('mounts a compiled root component instead of a runtime template', () => {
    const main = sourceFiles['./main.ts']

    expect(main).toContain("import App from './App.vue'")
    expect(main).toContain('createApp(App)')
    expect(main).not.toContain('template:')
  })

  it('uses the admin shell only for protected workspace routes', () => {
    const app = appFiles['./App.vue']

    expect(app).toContain("import AdminShell from './components/AdminShell.vue'")
    expect(app).toContain('route.meta.public')
    expect(app).toContain('<AdminShell')
  })

  it('keeps navigation out of individual workspace views', () => {
    for (const [path, source] of Object.entries(appFiles)) {
      if (!path.startsWith('./views/')) continue
      expect(source, path).not.toContain('<header class="topbar">')
    }
  })
})
