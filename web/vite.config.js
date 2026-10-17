import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src')
    }
  },
  server: {
    port: 5174,
    proxy: {
      '/api': {
        target: 'http://localhost:6050',
        changeOrigin: true
      }
    }
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true
  },
  // UI tests: the real views with the real store and API client in jsdom;
  // only HTTP answers, timers and confirm() are controlled. [MESHSAT-1584]
  test: {
    environment: 'jsdom',
    environmentOptions: { jsdom: { url: 'http://review.local/?shell=engineer' } },
    include: ['tests/**/*.test.js']
  }
})
