# Changelog

本文件记录 NexCore x-ui 各版本的关键变更。**用户视角**为主,实现细节看
对应 commit。日期为 release tag 创建时间,格式 ISO-8601。

版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/):

- 主版本 (MAJOR) — 不兼容的 API / 配置 schema 变更
- 次版本 (MINOR) — 向后兼容的功能新增
- 修订版本 (PATCH) — 向后兼容的 bug 修复 / 文档

完整 release 资产(含每架构 tarball + checksums.txt + SLSA attestation)在
[GitHub Releases](https://github.com/DoBestone/nexcore-x-ui/releases)。

---

## [v2.6.3] — 2026-05-09

**在线更新整体架构改造**

- **设计反思**:此前 v2.5.x ~ v2.6.2 的 ApplyLatest 在 Go 层 inline 实现了
  「下载 tarball → SHA256 → 解压 → 替换二进制 → restart」整套逻辑(~200 行),
  跟 update.sh 做的事**完全重复**,接连出过 v2.6.0(xray 子进程不收拾)和
  v2.6.2(还是双轨维护)两个生产 bug。
- **新方案**:面板按钮触发后,Go 层只做 release sanity check,然后用
  `systemd-run --unit=... --collect --no-block bash update.sh <tag>` 把更新
  动作派给 transient service(独立 cgroup)。脚本接管下载 / 校验 / 解压 /
  systemctl stop+start。systemd 自动 KillMode=control-group 收拾整个 cgroup,
  xray 子进程白送被清掉。
- 升级路径**收敛到一条**:install.sh / 操作员手动 / 面板按钮全用同一份
  update.sh,update.sh 改逻辑直接推 main 立刻生效,不用等 panel 重新发版。
- Go 层 update.go 净减 ~80 行(老 helper 暂留,后续清理)。

## [v2.6.2] — 2026-05-09

- **修在线更新「xray 报错 + 版本没变」**(被 v2.6.3 进一步根治):
  re-exec 前必须 `XrayService.StopXray()` 把子进程 SIGTERM 掉。`syscall.Exec`
  只换进程映像不杀子进程,不显式 stop 会导致老 xray 占着 inbound 端口,
  新 panel 再 spawn xray 直接 EADDRINUSE。

## [v2.6.1] — 2026-05-09

- **文档/测试 patch**:显式记录 `PATCH /api/v1/settings` 支持 nodeName /
  nodeAddress / onlineWebhook* / webPort 等字段,加 round-trip 回归测试。
  代码层面这条 API 从 v2.0 就支持,只是 docs 示例只给了 subAllowedHosts
  没人发现 nodeName 也能改。

## [v2.6.0] — 2026-05-08

- **新增「系统更新」侧栏页**(/system-update):版本对比卡 + 立即更新 +
  实时 el-steps 进度 + 历史更新日志 accordion(markdown 渲染,支持「安装
  此版本」回滚)。
- **关键 bug 修复**:`ApplyLatest` 之前只发 SIGHUP,main.go 收到后只重建
  `web.Server`,**进程仍跑旧二进制**。改为 `syscall.Exec` 用新磁盘文件替换
  进程映像,PID 不变,新 main() 冷启动加载新代码。这是「在线更新」从今
  天起真正生效的根因修复。
- **GLIBC 兼容性彻底解决**:`gorm.io/driver/sqlite` (cgo + mattn) →
  `glebarez/sqlite` (pure-Go modernc),release.yml 切 `CGO_ENABLED=0`,
  产物**完全静态**,运行时不依赖 glibc。修 v2.5.2 在 Ubuntu 20 / Debian 11
  上「GLIBC_2.34 not found」装不起来的现场。
- 新端点:GET /xui/api/update/{progress,releases};v1 镜像
  /system/update-{progress,releases}。

## [v2.5.2] — 2026-05-08

- Dashboard 新增「项目信息」卡(GitHub 仓库 + 协议)+ 9188.pro 推荐位。
- README 装机文档对齐,4 项 v1 API 改进(traffic/live ?reset、
  online-ips-by-email ?detailed、xray/template envelope、xray/logs ?kind)。

## [v2.5.1] — 2026-05-08

- `/api/v1/inbounds` 默认裁剪视图补回 `outboundTag` 字段。

## [v2.5.0] — 2026-05-08

- `/server/status` 速率永远 0 修复(挂 5s 主动 ticker 刷 lastStatus)。
- 客户端 QR API + 分享链接 API:`/inbounds/:id/links/by-email`、
  `/inbounds/:id/clients/:email/share`。

## [v2.4.x] — 2026-05-08

- **v2.4.0**:install-time webhook 回调(HMAC-SHA256 签名)— 适配云厂商
  一键部署场景,装机完毕后主动推送凭据 / 端口 / token 到指定 URL。
- **v2.4.1**:修 v2.4.0 编译失败(report.go 内联回 main.go)。

## [v2.3.0] — 2026-05-08

- 首装自动签发 admin-scope API token(写入 install banner)。
- `install.sh --secure-entry[=slug]` 一键开关「安全入口」,扫端口看到裸
  404 而不是登录页。

## [v2.2.x] — 2026-05-08

- **v2.2.0**:Outbound REST API + 三域 API 完整覆盖(inbound/outbound/
  client) + 公网 IP 自动探测。
- **v2.2.1**:`/api/v1/server/status` 网络速率永远 0 修复。

## [v2.1.x] — 2026-05-08

- **v2.1.0**:安全 + 并发完整 audit 加固 — 全套 cookie 加固、CSRF、bcrypt
  密码、会话固化、并发锁层加密。
- **v2.1.1**:首装健壮性 + `setting -show` panic-safe。
- **v2.1.2**:修 systemd SystemCallFilter 的 SIGSYS crash loop(Go 1.26
  的 prlimit64 / clone3 被 @resources 拦了)。
- **v2.1.3**:install-info.txt 文件取消,凭据只在 systemd journal 里
  (`journalctl -u nexcore-x-ui` 自查)。
- **v2.1.4**:systemd 自恢复加固 — StartLimitBurst 熔断 + network-online
  等待。

## [v2.0.x] — 2026-05-07

- **v2.0.0**:Vue 3 SPA 全量重构。前端 Element Plus + Vite + TypeScript。
- v2.0.1 ~ v2.0.16:per-email 流量统计、在线 IP TTL、复制链接、出站配置、
  per-client enable、节点名称、入站端口自动避让、安全入口骨架等密集迭代。

## [v1.x] — 2026-05-07 之前

基于 vaxilu/x-ui 的初始 fork,主要修补陈年 bug + 配套自部署脚本,详见
git log。从 v2.0.0 起进入 SPA + REST API 时代。

[v2.6.3]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.6.3
[v2.6.2]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.6.2
[v2.6.1]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.6.1
[v2.6.0]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.6.0
[v2.5.2]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.5.2
[v2.5.1]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.5.1
[v2.5.0]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.5.0
[v2.4.x]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.4.1
[v2.3.0]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.3.0
[v2.2.x]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.2.1
[v2.1.x]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.1.4
[v2.0.x]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v2.0.16
[v1.x]: https://github.com/DoBestone/nexcore-x-ui/releases/tag/v1.1.2
