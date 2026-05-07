import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import zhCn from 'element-plus/dist/locale/zh-cn.mjs'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'

import App from './App.vue'
import router from './router'
import './styles/global.css'

const app = createApp(App)

// Element Plus 全套图标注册一次,模板里直接 <el-icon><Plus /></el-icon> 用
for (const [name, comp] of Object.entries(ElementPlusIconsVue)) {
  app.component(name, comp as never)
}

app.use(createPinia())
app.use(router)
app.use(ElementPlus, { locale: zhCn })
app.mount('#app')
