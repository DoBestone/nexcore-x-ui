# Vue 3 SPA 迁移清单

> 状态:**规划中**,后期版本(建议 2.0)落地
> 最后更新:2026-05-07
> 当前栈:Go template + Vue 2.6.12 + ant-design-vue 1.7.2(非 SPA)
> 目标栈:Go API-only + Vue 3 + Vite + TS + Element Plus(SPA,embed 进单二进制)

---

## 一、背景与决策

### 当前现状
- 前端 8 个页面,共 ~2200 行 HTML/template:
  - `index.html`(418 行)、`inbounds.html`(410 行)、`api_console.html`(699 行)
  - `setting.html`(242 行)、`block_rules.html`(265 行)、`common_sider.html`(66 行)
  - `inbound_modal.html`(78 行)、`inbound_info_modal.html`(60 行)
- 已存在 `web/controller/api/v1.go` REST 层,但模板里仍混用 `c.HTML(...)` 渲染数据
- Vue 2 已 EOL(2023-12),ant-design-vue 1.x 长期不维护,但目前无紧急安全压力

### 重构理由
- 对齐 NexCore 产品线统一设计规范(`nexcore-ui` skill,Element Plus 明亮 SaaS 风格)
- 真正组件化、TS 类型安全、现代 DX
- 移动端响应式(当前 ant-design-vue 1.x 移动端体验差)

### 不立刻做的理由
- 1.x 仍在快速迭代功能(API 文档、block_rules、token 改造)
- 重构不仅是 UI——鉴权、API 层、embed FS、CI 链路都要动
- 每个面板功能都需回归测试,容易在"看起来差不多"的地方出回归

### 工作量预估
**8-12 个工作日**(含回归测试),建议放到 2.0 规划

---

## 二、阶段 0:决策 & 准备(0.5 天)

- [ ] 确认目标版本号(建议 `2.0.0`,语义化版本破坏性更新)
- [ ] 拉 `feat/vue3-spa` 长期分支,`main` 继续修 1.x bug
- [ ] 决定打包模式:**embed FS 单二进制**(推荐,符合现产品形态)还是反代分离
- [ ] 选 UI 库:**Element Plus**(对齐 `nexcore-ui` skill 规范)
- [ ] 备份当前 `web/html/xui/` 和 `web/assets/` 整目录

---

## 三、阶段 1:后端 API 化(2 天)

当前 `web/controller/xui.go / inbound.go / setting.go / api_panel.go` 一半返回 JSON 一半渲染模板,SPA 需要纯 JSON。

- [ ] 梳理所有 `c.HTML(...)` 调用点,记成清单
- [ ] 把模板渲染路由收敛到一个 catch-all,统一返回 `index.html`(SPA 入口)
- [ ] 业务接口全走 `/api/v1/*`,补齐缺失的:
  - [ ] `GET /api/v1/me`(当前用户/权限)
  - [ ] `GET /api/v1/server/status`(替代 index.html 内联数据)
  - [ ] `GET /api/v1/inbounds` 增删改查(检查 `inbound.go` 是否已 REST)
  - [ ] `GET/PUT /api/v1/settings`
  - [ ] `GET/POST /api/v1/block-rules`
  - [ ] `POST /api/v1/auth/login` + `/logout`
- [ ] 鉴权改造:**session cookie 仍可用**(SameSite=Lax),不必强上 JWT;但要保证 SPA fetch 带 `credentials: 'include'`
- [ ] 加全局 401 中断标识(让前端拦截器统一跳登录)
- [ ] CSRF:同源 cookie 模式下加 `X-Requested-With` 头校验即可,不要过度设计

---

## 四、阶段 2:前端项目搭建(0.5 天)

- [ ] `web/frontend/` 新建 Vite 项目:`npm create vite@latest -- --template vue-ts`
- [ ] 装栈:
  ```
  vue@3 + vue-router@4 + pinia + element-plus + axios
  + @vueuse/core + unplugin-auto-import + unplugin-vue-components
  ```
- [ ] 配 `vite.config.ts`:
  - [ ] `base: '/'`(子路径反代场景需运行时注入)
  - [ ] `build.outDir: '../dist'`(给 Go embed)
  - [ ] dev `proxy` 把 `/api` 转到 `http://localhost:54321`(或当前面板端口)
- [ ] 配 ESLint + Prettier,接入 `nexcore-ui` 设计 token(色板、间距、圆角)
- [ ] axios 实例:基址、401 拦截、错误 toast、loading 全局指示器

---

## 五、阶段 3:Go embed 集成(0.5 天)

- [ ] `web/web.go` 增加 `//go:embed all:frontend/dist` 的 FS
- [ ] 路由优先级:`/api/*` → 后端,其余 → embed FS;找不到文件落到 `index.html`(支持 `history` 模式)
- [ ] `nexcore-x-ui.sh` / `Makefile` 加 `make ui` 步骤:
  ```
  cd web/frontend && npm ci && npm run build
  ```
- [ ] `.github/workflows/release.yml` 在 Go build 之前跑前端 build(注意 Node 版本钉死,如 20.x)
- [ ] `.gitignore` 加 `web/frontend/dist/` 和 `node_modules/`

---

## 六、阶段 4:页面迁移(4-5 天)

**按这个顺序**,每页迁完立刻在浏览器跑一遍,不要堆到最后一起测。

- [ ] **登录页**(`index.html` 中的登录块剥离):最小依赖,先跑通认证闭环
- [ ] **布局壳**(`common_sider.html`):`<el-container>` + 侧栏 + 顶栏,接入路由
- [ ] **Dashboard**(`index.html` 主体 ~418 行):服务器状态、版本卡、Xray 控制
- [ ] **Inbounds**(`inbounds.html` + 两个 modal,~548 行):**最大头**,链接生成、二维码、订阅、流量统计都在这页
- [ ] **Settings**(`setting.html` ~242 行):分 tab,改密码、面板设置、Xray 模板
- [ ] **Block Rules**(`block_rules.html` ~265 行):规则增删改
- [ ] **API Console**(`api_console.html` ~699 行):Swagger/接口文档,可考虑直接换成 `vitepress` 或嵌 `@scalar/api-reference` 替代手写
- [ ] 公共组件抽离:
  - [ ] `<TrafficCell>` — 流量显示
  - [ ] `<QrcodeDialog>` — 二维码弹窗
  - [ ] `<ProtocolBadge>` — 协议徽章
  - [ ] `<JsonEditor>` — Monaco JSON 编辑器

---

## 七、阶段 5:i18n 与主题(0.5 天)

- [ ] 装 `vue-i18n@9`,搬运现有 `web/translation/` 的 zh/en/fa/...
- [ ] 接入 Element Plus locale 包
- [ ] 暗色模式:Element Plus CSS vars + `<html class="dark">` 切换
- [ ] 设计 token 来自 `nexcore-ui` skill,**不要现编色板**

---

## 八、阶段 6:回归测试(1.5 天)

逐项过,**不要"看起来正常"就算过**:

- [ ] 登录 / 改密 / 登出 / 会话过期跳转
- [ ] 多语言切换(含 RTL 阿拉伯/波斯语)
- [ ] 入站:VLESS / VMess / Trojan / SS / WS / gRPC / Reality 全协议增删改
- [ ] 链接生成 + 二维码 + 订阅 URL 正确
- [ ] 流量统计、客户端在线 IP、到期时间
- [ ] Xray 启停、配置模板保存、证书加载
- [ ] Block rules 增删改 + 实时生效
- [ ] API token 创建 / 撤销 / 限流
- [ ] 反代场景下子路径(`base_path`)是否正常
- [ ] 移动端响应式

---

## 九、阶段 7:发布(0.5 天)

- [ ] CHANGELOG 写明断点(浏览器最低版本、配置兼容性)
- [ ] 升级路径文档:1.x → 2.0 是否需要清理浏览器缓存、cookie 名是否变化
- [ ] beta tag 先发一版,在自有节点上跑 1 周
- [ ] 跟进 GitHub Issues 一周后再 promote 到 stable

---

## 十、风险点(优先盯)

1. **embed FS + history 路由的 fallback** — 配错会刷新 404,务必本地 build 后跑一遍
2. **base_path 子路径反代** — Vite `base` 必须支持运行时配置(从 Go 注入),否则用户改路径就白屏
3. **二维码 / clipboard / WebSocket** — 这几个原版用了非 ESM 库,迁移时优先找 Vue 3 友好替代(`vue-qrcode`、`@vueuse/core` clipboard)
4. **Monaco / 富文本编辑器**体积大 — 记得 `manualChunks` 拆包,否则首屏几 MB
5. **CI 里 Node 内存** — 大型前端 build 在 GitHub runner 偶发 OOM,加 `NODE_OPTIONS=--max-old-space-size=4096`

---

## 十一、不要走的中间路线

**不要"一半 SPA 一半 template"**——那是最糟的状态:
- 鉴权要写两套
- 路由跳转混乱(SPA 路由 vs Go 路由)
- 静态资源加载冲突
- 调试和回归测试成本翻倍

要么不动,要么一次做完。

---

## 十二、轻量替代方案(如果 8-12 天太长)

如果某个时间点想"小步升级"而非完整重构:

- 升 **Vue 2.7**(兼容 composition API,Vue 2 最后一个 LTS)
- 抽公共组件,不动构建链路
- 工作量 1-2 天,但只是拖延,不解决根本问题

最终还是要走 Vue 3 SPA。
