import { describe, expect, it } from 'vitest'

const sourceFiles = import.meta.glob('./main.ts', {
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
})
