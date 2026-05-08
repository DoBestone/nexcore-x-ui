<!--
谢谢提 PR。下面这几栏一两句话写完即可,不用写论文。
-->

## 这个 PR 干了什么

<!-- 一两句话。why > what,what 看 diff 就够了。 -->

## 关联 issue

<!-- 如有: Fixes #123 / Refs #456 -->

## 测试

<!-- 跑了什么验证?例: -->
<!-- - [x] go test ./... 全绿 -->
<!-- - [x] 装到 amd64 节点,面板「系统更新」按钮验证一遍 -->
<!-- - [x] curl PATCH /api/v1/settings 回归 nodeName -->

## Checklist

- [ ] commit message 用中文短句,符合现有 git log 风格
- [ ] 如果改动涉及 API,`docs/API.md` 跟 `web/controller/docs/api.md` 同步更新
- [ ] 如果改动涉及前端,`npm run build` 编译通过
- [ ] 没改 `config/version` / 没自己打 tag(发版由维护者控制)
- [ ] 没改 `.github/workflows/*` 除非这就是 PR 主旨

<!--
设计问题先在 issue / Discussion 谈再写代码,免得 PR 实现完再被打回方向。
有疑问就在 PR 描述里 @ 维护者。
-->
