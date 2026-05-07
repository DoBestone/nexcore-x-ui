import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// dev 时本地面板默认监听 54321。可用 NEXCORE_PANEL_PORT 覆盖。
const PANEL_PORT = process.env.NEXCORE_PANEL_PORT || '54321'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  // history-mode router — assets 必须用绝对路径,SPA 在 / 下挂载
  base: '/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1024,
    rollupOptions: {
      output: {
        manualChunks: {
          'element-plus': ['element-plus', '@element-plus/icons-vue'],
          'vue-vendor': ['vue', 'vue-router', 'pinia']
        }
      }
    }
  },
  server: {
    port: 5173,
    proxy: {
      '/login':   `http://127.0.0.1:${PANEL_PORT}`,
      '/logout':  `http://127.0.0.1:${PANEL_PORT}`,
      '/xui':     `http://127.0.0.1:${PANEL_PORT}`,
      '/api':     `http://127.0.0.1:${PANEL_PORT}`,
      '/server':  `http://127.0.0.1:${PANEL_PORT}`
    }
  }
})
