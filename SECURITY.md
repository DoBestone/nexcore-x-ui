# 安全策略 / Security Policy

## 受支持版本

| 版本 | 支持状态 |
|---|---|
| `v2.6.x` | ✅ 持续维护,推荐 |
| `v2.5.x` | ⚠️ 建议升级 — 已知 GLIBC 兼容性问题(v2.6.0 修复) |
| `v2.4.x` 及以下 | ❌ 不再维护 |

通过 [面板内「系统更新」按钮](https://github.com/DoBestone/nexcore-x-ui#系统更新) 或

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/DoBestone/nexcore-x-ui/main/update.sh)
```

升级到最新版。

---

## 报告漏洞

**请不要在公开 issue 里贴漏洞细节。** 走以下任意一条非公开渠道:

1. **GitHub Private Vulnerability Reporting**(推荐):
   <https://github.com/DoBestone/nexcore-x-ui/security/advisories/new>
2. **Email**:`security@9188.pro`,标题前缀 `[security]`。

请在报告中尽量包含:

- 受影响版本号(`nexcore-x-ui -v`)
- 攻击前置条件(需要 panel session?需要本地 root?需要特定配置?)
- 复现步骤(curl / 截图均可)
- 你认为的影响等级(信息泄漏 / 提权 / RCE / DoS)

我们会:

1. **48 小时内回执**确认收到。
2. 在 7 天内给出影响评估和修复时间窗。
3. 修复后协调公开披露(默认 90 天 disclosure window;关键漏洞会更快)。
4. 公开致谢愿意挂名的报告人。

---

## 已知威胁模型 / 设计取舍

NexCore x-ui 在以下假设下运行,与该假设不符的"漏洞"我们会标记为 **设计选择** 而非漏洞:

- **panel + xray 都以 root 身份运行**。xray 需要绑定特权端口、读 `/root/cert/`,
  vaxilu/x-ui 上游就是这个模型;面板自己也用 root 跑 systemctl 操作 xray
  unit。systemd unit 里挂了一系列 hardening flags(`ProtectSystem=strict`、
  `NoNewPrivileges=true`、`SystemCallFilter=@system-service` 等)做防御
  纵深,但完整 root 妥协不是 in-scope。
- **首装凭据明文一次性显示**(install banner / journal),错过即不可回查。
  v2.1.3+ 默认不再落盘 `install-info.txt` 文件。
- **DB 文件 0600 + dir 0700 root 持有**。SQLite 存了 settings(含加密的
  webhook secret / cf token / TG token),分享链接里的客户端凭据(uuid /
  password)以 raw JSON 存于 inbounds 表。**非 root 用户在同一台机器上不能
  读 DB**;丢机 = 丢全部凭据 — 这是接受的语义。
- **API token 仅以 SHA256 哈希存储**,丢失只能 rotate。Legacy single-token
  + multi-token 表共存,bearer auth 优先匹配 multi-token。
- **panel 自己的 HTTP 服务**:CSRF 双 cookie + SameSite=Lax + HttpOnly +
  HSTS(TLS 启用时),登录走 bcrypt cost 12,失败 5 次锁 IP 60s,会话固化
  (login 后重签 session id)。这些是 in-scope。

如果你认为某条策略本身就是错的(而不是某次实现 bug),也欢迎以 GitHub
Discussion 形式提出讨论。
