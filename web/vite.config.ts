import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': 'http://localhost:8088'
    }
  },
  build: {
    // BaoTa may place .user.ini in dist; preserve it during frontend rebuilds.
    emptyOutDir: false
  }
})
