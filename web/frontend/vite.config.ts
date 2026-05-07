import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import { fileURLToPath, URL } from 'node:url'

// dev 时本地面板默认监听 54321。可用 NEXCORE_PANEL_PORT 覆盖。
const PANEL_PORT = process.env.NEXCORE_PANEL_PORT || '54321'

export default defineConfig({
  plugins: [
    vue(),
    // Element Plus 按需引入。
    //
    // 旧实现:main.ts 全量 `import ElementPlus from 'element-plus'` +
    //         全套图标 `app.component(name, ...)` 循环注册。
    // 旧产物:vendor 1.05MB JS + 357KB CSS = 1.6MB total。
    //
    // 新实现:模板里直接写 <el-button>、<el-icon><User /></el-icon>,
    //         resolver 在编译期把这些组件 / 图标按用到的部分逐个 import,
    //         未用到的部分被 Vite tree-shake 掉。CSS 同步分块,只打包
    //         实际渲染过的组件样式。
    // 新产物:vendor 约 ~400-600KB(随用到的组件多寡浮动)。
    //
    // 注意:全局 message / dialog / loading 等 imperative API(ElMessage、
    // ElMessageBox、ElLoading)仍是直接 import 用,不走 resolver —
    // 它们没有 template 出现位置,resolver 看不到。这条早就在做。
    AutoImport({
      resolvers: [ElementPlusResolver()]
    }),
    Components({
      resolvers: [ElementPlusResolver()]
    })
  ],
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
    target: 'es2020',
    rollupOptions: {
      output: {
        manualChunks: {
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
      '/server':  `http://127.0.0.1:${PANEL_PORT}`,
      '/panel-login': `http://127.0.0.1:${PANEL_PORT}`
    }
  }
})
