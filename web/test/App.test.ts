import { describe, expect, it } from 'vitest'

const sourceFiles = import.meta.glob('/src/main.ts', {
  eager: true,
  import: 'default',
  query: '?raw'
}) as Record<string, string>

const appFiles = import.meta.glob([
  '/src/App.vue',
  '/src/components/AdminShell.vue',
  '/src/views/Dashboard.vue',
  '/src/views/Agents.vue',
  '/src/views/TaskDetail.vue',
  '/src/views/MergeRequests.vue',
  '/src/views/Issues.vue',
  '/src/views/Deliveries.vue',
  '/src/views/Pipelines.vue'
], {
  eager: true,
  import: 'default',
  query: '?raw'
}) as Record<string, string>

describe('root application', () => {
  it('mounts a compiled root component instead of a runtime template', () => {
    const main = sourceFiles['/src/main.ts']

    expect(main).toContain("import App from './App.vue'")
    expect(main).toContain('createApp(App)')
    expect(main).not.toContain('template:')
  })

  it('uses the admin shell only for protected workspace routes', () => {
    const app = appFiles['/src/App.vue']

    expect(app).toContain("import AdminShell from './components/AdminShell.vue'")
    expect(app).toContain('route.meta.public')
    expect(app).toContain('<AdminShell')
  })

  it('uses Element Plus adaptive menu components for workspace navigation', () => {
    const shell = appFiles['/src/components/AdminShell.vue']

    expect(shell).toContain('<el-container')
    expect(shell).toContain('<el-aside')
    expect(shell).toContain('<el-menu')
    expect(shell).toContain('<el-menu-item')
  })

  it('keeps navigation out of individual workspace views', () => {
    for (const [path, source] of Object.entries(appFiles)) {
      if (!path.startsWith('/src/views/')) continue
      expect(source, path).not.toContain('<header class="topbar">')
    }
  })
})
