/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<{}, {}, any>
  export default component
}

// Element Plus 把 locale 包发布为 .mjs,但没附带 .d.ts;
// 不需要类型,声明成模块即可。
declare module 'element-plus/dist/locale/zh-cn.mjs'
