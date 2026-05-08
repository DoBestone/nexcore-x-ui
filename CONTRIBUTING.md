# Contributing to NexCore x-ui

欢迎贡献!这份文档说明仓库的协作规约 — 看完几分钟,提 PR / issue 都顺畅。

## 提 issue 前先看

1. **不是 bug 别开 issue**。配置写错了、客户端连不上、想知道怎么部署
   ⇒ 走 [Discussions](https://github.com/DoBestone/nexcore-x-ui/discussions)。
2. **搜一下** [现有 issues](https://github.com/DoBestone/nexcore-x-ui/issues?q=) — 大概率已经有人提过。
3. **issue 模板**会要求你填:版本号(`nexcore-x-ui -v`)、复现步骤、期望
   行为、实际行为、systemd journal 片段。**这几项缺任一都很难推进**,
   作者会先关掉等补充。

## 提 PR

### 流程

1. Fork 仓库,基于 `main` 切分支(分支名描述意图,如 `fix/xray-port-conflict`)。
2. 改动配测试。Go 代码用 `go test ./...` 跑全;前端用 `npm run build`
   保证编译通过(我们没强制单测覆盖率,但坏掉的回归至少补一条防回归用例)。
3. **commit message 用中文短句**,跟 git log 里现有风格保持一致:
   `feat: v2.x.y — 一句话总结` / `fix: 一句话总结`。
4. 推上来,开 PR 指向 `main`。PR 描述里写**为什么**(不是 what),what
   看 diff 就够了。
5. CI 会跑 build。绿了等 review。

### 别这么做

- 别在 PR 里改 `config/version` 或打 tag — 发版由维护者控制(走
  release.yml 自动发 4 架构 tarball)。
- 别在不相关的 PR 里顺手 reformat 整个文件,review 噪音太大。
- 别加 `// removed` 这种墓碑注释 — 删了就删了,git log 能查。
- 别加只为「以防万一」的 error handling / 验证逻辑 / feature flag。
  实际触发不了的分支就是死代码,加完只让 review 更难。
- 别把面板默认行为变成「需要客户操作员自己改才能用」— 系统层报错就修
  代码,不要让用户自己 workaround。

### 风格速查

- **Go**:`gofmt`(`go fmt ./...`)+ `go vet ./...` 必须干净。包名小写,
  exported 函数才大写。错误用 `fmt.Errorf("xxx: %w", err)` 包,不要丢
  context。
- **Vue 3 / TypeScript**:Composition API + `<script setup>`,不写 Options
  API。Element Plus 组件按需,别引整包。
- **注释**:写 *为什么* (设计取舍 / 避坑 / 关联 incident),不写 *是什么*
  (代码本身能读出来)。注释要解释非显然的约束 / 隐藏 invariant /
  surprising 行为,不要复述函数名。
- **API**:`/api/v1/*` 是稳定面,加新端点 OK,删旧端点要走 deprecation
  cycle(打 warning,至少一个 minor 版本后再移除)。

## 本地开发

```bash
git clone https://github.com/DoBestone/nexcore-x-ui.git
cd nexcore-x-ui

# 后端
go mod download
go build .
./nexcore-x-ui setting -show          # 看默认配置

# 前端(Vue 3 SPA)
cd web/frontend
npm ci
npm run dev                           # vite dev server
npm run build                         # 产物嵌入 Go binary 走 //go:embed
```

测试:

```bash
go test ./...                         # 跑全 suite
go test ./web/service/ -count=1       # 不缓存
```

## 行为准则

1. 对事不对人。代码烂可以说,但不评价提 PR 的人。
2. 中文 / 英文 issue 都接受。维护者用中文回复中文 issue,英文回复英文。
3. 政治、宗教、立场无关讨论一律关闭。这是个技术项目。

---

不知道从哪开始?[good first issue 标签](https://github.com/DoBestone/nexcore-x-ui/issues?q=is:open+label:%22good+first+issue%22) 是入门起点。
