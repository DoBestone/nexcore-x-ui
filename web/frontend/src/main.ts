import { createApp } from 'vue'
import { createPinia } from 'pinia'

// Element Plus 按需引入。unplugin-vue-components 配合 ElementPlusResolver
// 在编译期把每个 <el-*> 组件需要的样式 chunk 单独 import,Vite 把它们
// 切成独立 CSS 文件按需加载。我们故意 NOT 全量 import 'element-plus/
// dist/index.css' —— 那一刀会立即把整个 357KB css 拉回首屏,抵消所有
// 按需引入的体积收益。
//
// 仅保留 dark mode CSS 变量(theme-chalk/dark/css-vars.css):它只是几
// kb 的 CSS variable 声明,而且独立于具体组件,resolver 不会自动加载,
// 全局 import 一次最简单。如果未来不打算开 dark mode,这一行也可以删。
import 'element-plus/theme-chalk/dark/css-vars.css'

// Imperative API(ElMessage / ElMessageBox)在 JS 里直接 import 调用,
// 模板里没有 <el-message-box> 标签,resolver 看不到 → 不会自动注入 CSS,
// 弹出来就是无样式的“裸” DOM。这里手动按需引入样式入口(message-box
// 的 css.mjs 会传递依赖 base/overlay/input/button 等)。
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'

import App from './App.vue'
import router from './router'
import './styles/global.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')
