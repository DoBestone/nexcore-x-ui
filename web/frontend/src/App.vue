<script setup lang="ts">
import { RouterView } from 'vue-router'
import { ElConfigProvider } from 'element-plus'
import zhCn from 'element-plus/dist/locale/zh-cn.mjs'
</script>

<template>
  <!--
    ElConfigProvider 注入 Element Plus 的 i18n locale,等价于旧 main.ts 里的
    app.use(ElementPlus, { locale: zhCn })。改用这种方式后 ElementPlus 本身
    不再被全量注册,所有 <el-*> 组件改由 unplugin-vue-components 的 resolver
    按需 import,从而得到 tree-shaking。
  -->
  <ElConfigProvider :locale="zhCn">
    <RouterView />
  </ElConfigProvider>
</template>
