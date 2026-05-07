# NexCore X-UI · Claude 上下文备忘

> 给未来的 Claude(和我自己)看的项目档案。
> 不重复 README 里能查到的安装/使用,只记**架构决策、踩坑、约定**。
> 最后更新:2026-05-07

---

## 项目定位

3x-ui 系 Xray 面板 fork,目标是 **NexCore 产品线统一控制台**之一。
当前版本 `1.0.1`,处于功能快速迭代期。

部署目标机型:**1 核 1G VPS**(节点机,通常很便宜很弱)。
所有架构决策都要回到这个约束:**资源紧 + IO 差 + 经常断电重启 + 用户不会调优**。

---

## 已锁定的架构决策(不要再讨论)

### 1. 后端语言:**保持 Go**,不重构 Rust
- Xray-core 本身是 Go,直接 import 当库
- Rust 没有对等生态(`v2ray-rust` 远不及 Xray-core)
- 重写 2-3 个月,断了 3x-ui 上游 cherry-pick 通道
- **唯一例外**:如果哪天换内核(不再依赖 Xray-core),才重新评估

### 2. 数据库:**保持 SQLite**,不换 PG/MySQL/KV
- 1G 内存机器 PG 空闲就吃 80-150MB,Xray 高峰必 OOM
- BoltDB/Badger 没 SQL,流量聚合要重写,改造成本极高
- DuckDB 是 OLAP,反向优化
- SQLite 在嵌入式场景**就是最优解**,不是穷人版
- **驱动可选迁移**:`mattn/go-sqlite3`(CGO)→ `modernc.org/sqlite`(纯 Go),好处是 cross-compile 简化,代价是 10-20% 性能下降(1H1G 上感知不到)。**待定**,不急。

### 3. 前端:**Vue 3 SPA 全量重构(2026-05-07 决策修订)**
- 现状:`web/html/xui/*.html` Go template + 内嵌 Vue 2 + ant-design-vue 1.x(CDN,无构建)。
- 目标:Vite + Vue 3 + TypeScript + Element Plus(对齐 nexcore-ui 设计规范)纯 SPA。
- 落地分支:`main` 直接进(单人项目无评审压力,分支模型徒增切换成本)。后端 API 已经齐(`/xui/api/*` panel session-auth + `/api/v1/*` token-auth),前端只换壳。
- 完整迁移清单见 [`docs/vue3-spa-migration.md`](docs/vue3-spa-migration.md)。

**决策修订(2026-05-07 当日)**:用户明确要求"现在直接执行 vue3 全面升级,不要混用"。
旧决策("先把 1.x 做稳再说")**失效**的理由:
1. 1.x 期间持续在 Vue 2 模板里加新功能(per-client modal、双卡片入站、添加客户端 Form),
   每加一块都在 ~2200 行 HTML 里堆,可读性 / 维护性已经是负资产
2. 当前会话里 modal 模板和外层 Vue 实例耦合(`ctModal` 全局变量穿透),Vue 2 的
   sprinkled 写法没法收敛成组件树,改一处影响一片
3. 用户原话"实在受不了"——继续在旧栈打补丁的体验成本已经超过重写收益预期
4. nexcore-ui 设计规范是 Element Plus + Vue 3 + TS 栈,X-UI 不切就永远融不进产品线

**落地必须遵守**:
- **不要走中间路线**(一半 SPA 一半 template)——鉴权/路由/资源会全部混乱;要切就一刀切
- 后端鉴权层不动:仍然 cookie session,SPA fetch 时直接走 same-origin
- 静态资源构建产物嵌入 Go binary(`go:embed web/frontend/dist`),保持单文件部署

---

## 数据库优化进度(2026-05-07 起)

> 所有优化围绕 1H1G VPS 的**写入瓶颈 + IO 瓶颈**展开,不是吞吐型 SaaS 那一套。

### ✅ P0 已完成(2026-05-07)

`database/db.go`:
- DSN 增加:`_synchronous=NORMAL` / `_cache_size=-20000` / `_temp_store=MEMORY`
- 连接池:`SetMaxOpenConns(1)` + `SetMaxIdleConns(1)`(SQLite 单写入者模型)
- 故意**不**开 `_mmap_size`——避免和 Xray 抢内存触发 OOM-killer
- 故意**不**升 `_cache_size` 到 50MB+——同样的内存压力问题

预期收益:写入吞吐 3-5x,流量统计聚合查询提速明显。

### 🟡 P1 待做(下一批)

1. **流量 / 在线 IP 写入批量化**
   - 当前每条记录一个事务 = 每条一次 fsync
   - 改为内存聚合 1-3 秒 + 单事务批量 flush
   - 在线 IP 用 `INSERT ... ON CONFLICT DO UPDATE`(SQLite 3.24+)
   - 涉及文件:`web/service/xray.go`、`web/service/online_ip_service.go`

2. **索引审计**
   - 检查 `inbound_client_ips` 是否有 `(client_email, ip)` 联合索引
   - 检查 `client_traffics` 是否有 `email` 索引
   - 检查 `inbounds` 是否有 `(enable, protocol)` 联合索引
   - 涉及文件:`database/migrations.go`

### 🟢 P2 待做(收益较小)

3. **WAL checkpoint 定时器**
   - 每 5 分钟跑 `PRAGMA wal_checkpoint(PASSIVE)`
   - 防止 `*.db-wal` 文件无限膨胀

4. **新部署 auto_vacuum INCREMENTAL**
   - 仅对**新建库**生效,老库不要在线 VACUUM(会卡几十秒)
   - 涉及文件:`database/migrations.go` 初次建库分支

---

## 编码约定

### 风格
- 保持当前代码注释风格:**多行注释解释 WHY 和踩坑历史**(`database/db.go` 顶部 DSN 注释是范本)
- 不要为了"简洁"删掉解释决策动机的注释——那些是给三个月后的自己看的

### 安全相关
- 数据目录权限 `0700`,DB 文件 `0600`(`database/db.go` 已实现)
- `util/secret/` 是 at-rest 加密层,所有敏感字段(token、magic_token、settings 中的密码)走它
- 改密码会让所有现有 token 失效——这是**特性不是 bug**(参考 commit `a6a7633`)

### 数据库相关
- **任何新表**:加索引前先想清楚查询模式,不要无脑加
- **任何高频写**:必须批量化 + 单事务,不要一条一事务
- **任何 schema 变更**:走 `database/migrations.go`,不要手动 SQL
- **不要**在生产路径调 `VACUUM`(全量,会卡)
- **可以**调 `PRAGMA incremental_vacuum(N)`(增量,前提是 auto_vacuum=INCREMENTAL)

### 前端相关
- 修 bug / 加小功能 → 在现有 `web/html/xui/*.html` 上改
- 不要悄悄引入 Vue 3 / Vite / TS——**要做就一次做完**(走迁移清单)
- 新增静态资源走 `web/assets/`,沿用现有目录命名规范

### 发布相关
- 版本号在 `config/` + `nexcore-x-ui.sh` + 仪表盘版本卡多处出现,**统一更新**(参考 commit `eb97138` / `a164dc6`)
- CI 用 `setup-go@1.26` 对齐 `go.mod`(踩过坑,见 `a164dc6`)
- Release asset 命名规则不要乱改,在线升级会按命名匹配
- **release tarball 解压后必须 chown root:root**——CI runner 是 uid 1001,
  `cp -a` / `tar` 保留这个 uid,但 `xray.preflightBinary` 要求 owner=root,
  否则 xray 永不启动循环报 "owned by uid 1001"。三条升级路径
  (`update.sh` / `nexcore-x-ui.sh::cmd_update` / `service/update.go::ApplyLatest`)
  全都要 chown,任一漏掉就重现 v1.0.5 故障(commit `e604675`)

### HTTP / Middleware 相关 — **非常容易踩,先看这条**

任何**包装 `gin.ResponseWriter` 的 middleware**(gzip、metrics、内容改写等):

- **必须在 `c.Next()` 返回后 flush 任何缓冲**。Gin **不会**自动调
  `Flush()` —— 只有 SSE/streaming 路径才调。普通 handler 写完就返回,
  buffer 里的数据**永远没机会出去**
- **不要**只调 `gz.Close()` / `encoder.Close()` 就走人,要确保 wrapper
  的内部缓冲也 flush 到底层 `ResponseWriter`
- **任何"小响应跳过压缩 / 跳过加密 / 跳过改写"的优化分支** —— 要在分支上明确把
  原始 bytes 写到 socket,不能 silently 吞掉
- 写 wrapper 必带 6 条最小回归测试(参考 `web/gzip_middleware_test.go`):
  · 小响应 (<1KB)、大响应 (≥1KB)、客户端不要该编码、已编码内容旁路、
  204 空响应不崩、handler 多次 Write 字节累加完整

**踩坑历史**:`v1.0.4` 引入 gzipMiddleware 时只测了大响应,小响应 buffered
没 flush 被吞掉,导致 v1.0.5/v1.0.6 用户 ERR_CONTENT_LENGTH_MISMATCH 一片
(commit `16c3f42`)。任何 ResponseWriter 包装都要照着这条 checklist 写测试,
**不要相信"看起来对就行"**。

### Xray stats — user 级流量必须三件套齐全

per-email 流量统计(`client_traffics.up/down`)只有同时满足以下三条才工作:

1. **policy 开 user 级**:`policy.levels.0.statsUserUplink/Downlink = true`
   (在 `web/service/config.json`)。光开 `system.statsInboundUplink` 是不够的,
   那只给 inbound 聚合,xray gRPC 不会暴露 per-email 计数器
2. **trafficRegex 匹配 user 分支**:`xray/process.go` 正则必须包含 `user`,
   形如 `(inbound|outbound|user)>>>([^>]+)>>>traffic>>>(downlink|uplink)`
3. **client_traffics 行存在**:`syncEmbeddedClientTraffics` 在 AddInbound /
   UpdateInbound 时同步,`SyncAllClientTraffics` 启动时兜底老库。否则
   `AddTrafficByEmail` 收到流量但 UPDATE WHERE email=... RowsAffected=0,
   流量被悄悄丢

**踩坑历史**:`v1.1.x` 多次出现"入站累计有流量,客户行流量永远 0",原因都
是上面三条缺一。trafficRegex 是最隐蔽的——policy 给了它会无声忽略 `user>>>`
key,因为不匹配。

### 在线 IP TTL — access-log-tail 的固有局限

`web/service/online_ip_service.go` 通过 tail xray access.log 抽 `accepted ...
[tag -> direct] email: foo` 行做在线状态。**只在 connection accept 那一刻
有日志**,长连接(xtls-rprx-vision 视频/挂代理)建立后没有新行。所以 TTL
不能太短,否则用户挂着代理 modal 上一直闪"在线/离线"。

当前 5 分钟,够覆盖大多数浏览短连接 + 长流活跃心跳。要"实时准确"得用
xray `statsservice` query `user>>>email>>>online`,但需要 xray-core 编译
开关,先不做。

---

## 文档索引

- [`docs/vue3-spa-migration.md`](docs/vue3-spa-migration.md) — Vue 3 SPA 完整迁移清单(后期使用)
- 本文件 — 架构决策 + 优化进度
- `README.md` — 用户向安装使用文档(不要把架构内部决策塞这里)

---

## 给 Claude 的工作约定

1. **回答性能/架构问题前先看本文件**——已锁定的决策不要再"二选一"地讨论
2. **触发"要不要换 X"类问题**(数据库/语言/框架)→ 翻"已锁定的架构决策",直接给结论 + 引用
3. **新增 P 级任务**→ 更新本文件的"数据库优化进度"或对应章节,不要散落在 commit message
4. **发现新踩坑**→ 写进"编码约定",带上 commit hash 当证据
5. **架构决策反转**(罕见)→ 不要直接改,在新章节"决策修订"里写明 **何时 / 为什么 / 旧决策为何失效**
