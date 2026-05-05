# bot/qq-bind — QQ 绑定 / 解绑流程 + 加急通路

> 父：`bot/SKILL.md`。  
> 兄弟：`account-model.md`（账号模型）、`orphan.md`（孤儿行为）。
>
> 本文件覆盖：
> 1. **QQ 绑定 / 解绑** —— hfut <-> bot 的对称服务通路
> 2. **加急通路（P3.3）** —— 同一服务通路上的反向用法

---

## QQ 绑定流程总览

```
[App] 用户输入要绑的 QQ 号
   ↓
[hfut] check_friend(qq=X)  →  调 bot 的内部 HTTP API
   ├─ 否 → 提示用户先加 bot 为好友
   └─ 是 → 生成 6 位验证码，存 redis（key=qq_bind_code:{X}, ttl=5min, value={code, requesting_user_id}）
                ↓
        [hfut → bot] send_private(qq=X, text="验证码: 123456，5min 内有效")
                ↓
        [App] 用户输入验证码（输入框只允许 6 位数字）
                ↓
        [hfut] 校验：redis 里 code 匹配 + requesting_user_id 匹配 → 命中
                ↓
        旗下账号挂载（孤儿挂上 / 创建空的） + 学校信息处理 + tx commit
                ↓
        前端显示"绑定成功"，跳转个人主页
```

---

## bot 端：反向 HTTP API server（`internal/api/server.go`）

- 端口：`BOT_INTERNAL_API_PORT`（默认 8090）；docker 部署**不要 ports 映射到宿主机**，仅 internal network 暴露
- 鉴权：服务间 JWT，跟 bot → hfut 方向**对称**——
  - 共享 HS256 secret = `HFUT_API_JWT_SECRET`（bot 端 env） / `BOT_SERVICE_JWT_SECRET`（hfut 端 env），同一个值
  - hfut 每次自签 60s 有效期 JWT，放 `X-Service-Token` 头
  - iss = `HFUT-Graduation-Project-hfut`（跟反向 `HFUT-Graduation-Project-bot` 不同——防止两个方向的 token 互换使用）
  - secret 空时 server 不启动（安全降级）
- 路由：

| Method + Path | Body | Resp |
|---|---|---|
| `POST /internal/qq/check-friend` | `{qq_number, no_cache?}` | `{is_friend: bool}` |
| `POST /internal/qq/send-private` | `{qq_number, text}` | `{message_id}` |
| `POST /internal/qq/send-group` | `{group_id, qq_number?, text}` | `{status: "ok"}`（qq_number=0 时纯文本群消息，否则 @ 该用户） |
| `GET /internal/healthz` | — | 不需要 token，给 docker compose healthcheck 用 |

---

## hfut 端：4 个 RESTful 端点（`controller/qq_bind.go`，走 user JWT）

绑定 / 解绑**都走 request-code → confirm 两步**，对称设计——解绑也要 QQ 端能收到验证码才能成功，防主账号 token 被盗后攻击者"解绑别人 + 自己重新绑 + 盗取旗下账号数据"的攻击向量。

| Method + Path | Body | Resp |
|---|---|---|
| `POST /api/v1/user/qq-bind/request-code` | `{qq_number}` | `{ttl_seconds: 300}`（hfut 调 bot CheckFriend → 是好友再发码） |
| `POST /api/v1/user/qq-bind/confirm` | `{qq_number, code}` | 200/400 |
| `POST /api/v1/user/qq-unbind/request-code` | 无 body（自动取当前绑定的 QQ） | `{ttl_seconds: 300}` |
| `POST /api/v1/user/qq-unbind/confirm` | `{code}` | 200/400 |

校验顺序（每个写入端点都走）：

1. 主账号是 `account_type=normal` 且 `school_id != 0`（先绑学校再绑 QQ）
2. 当前主账号下没有挂着的旗下账号（严格 1:1）
3. 验证码逻辑：

请求验证码时调用 bot.CheckFriend；不是好友 → 404；是 → redis 存 `qq_bind_code:{qq}=={code, requesting_user_id, created_at}` TTL 5min，再调 bot.SendPrivate 发到 QQ。

确认时校验 redis：code + requesting_user_id 匹配；命中 → tx 内挂载（找现有孤儿旗下账号 → 设 parent_user_id + 用主账号 school_id 覆盖；找不到 → 创建空旗下账号直接挂上）。tx commit 后删 redis code。

解绑：跟绑定对称的两步走——`request-code` 阶段从主账号查出当前绑定的 QQ，调 bot 给那个 QQ 发"解绑确认验证码"私聊；`confirm` 阶段校验 code 后真把 `parent_user_id` 设回 NULL，**不删任何数据**——旗下账号的商品 / 提问继续存在但变孤儿。

解绑文案故意区别于绑定（"您正在**解除**当前 QQ 与 app 账号的绑定" + "如非本人操作请忽略此消息——可能是你的 app 账号被盗"），让用户清楚操作意图、能识别异常。

解绑限流跟绑定**独立**（不同 redis key 前缀 `qq_unbind_throttle:` / `qq_bind_throttle:`）——避免用户绑定刚撞限流就也走不了解绑这种死锁。

---

## 成功通知（在 confirm 完之后再发一次私聊）

绑定 / 解绑 confirm 成功之后，bot 都会**再发一次私聊确认通知**——让用户感知账号变更，发现异常时立刻反应：

| 时机 | 私聊文案 |
|---|---|
| 绑定成功 | "绑定成功 ✅ 当前 QQ XXX 已成功绑定到 app 账号 \"username\"。如非本人操作，请立即在 app 里解绑并修改密码。" |
| 解绑成功 | "解绑成功 ✅ QQ XXX 已与 app 账号 \"username\" 解除绑定，旗下账号变成孤儿状态（商品/提问数据保留）。如非本人操作，请立即修改 app 密码并重新绑定。" |

实现要点：

- 通知**异步**于父 ctx 发出（独立 30s timeout），前端接口已经响应也会继续发
- 通知失败仅 log warn，**不回滚** DB——绑定结果以事务为准，通知是辅助
- 这是一种"账号安全提醒"——参考银行卡变更短信通知的设计，让用户即便被盗号也能从 QQ 端发现

---

## 限流 / 错码锁（防爆破）

### 限流（验证码请求频次）

- 同一 user 60 秒内最多请求 1 次验证码（redis SetNX `qq_bind_throttle:{user_id}` TTL 60s）
- 跟前端"获取验证码"按钮的倒计时对齐——常规体验
- 每次新请求会显式 Del 旧的 `qq_bind_code:{qq}`（同 QQ 的旧验证码立即失效），避免歧义

### 错码锁（防爆破）

- 计数器 key：`qq_bind_fail:{qq}` / `qq_unbind_fail:{qq}`，每次错码 INCR + EXPIRE 10min（滑动窗口）
- 锁 key：`qq_bind_lock:{qq}` / `qq_unbind_lock:{qq}`，达到 5 次错码后 SetNX TTL 30min
- check 入口：`RequestCode` 与 `Confirm` 两条路径都先 `checkBindLock` 命中即拒；锁住情况下连"获取验证码"都不允许，避免攻击者通过定时探测试探 code 是否存在
- 成功 confirm 后 `clearBindFailures` 一并清掉计数器和锁

---

## 错误回执（前端按需提示）

| 状态码 | sentinel | 用户感知 |
|---|---|---|
| 400 | `ErrQQNumberInvalid` / `ErrUserNotBoundSchool` / `ErrUserAlreadyBoundQQ` | 提示具体原因（"请先绑学校"/"已绑过 QQ"等） |
| 400 | `ErrCodeInvalid` / `ErrCodeExpired` | "验证码错误 / 已过期，请重试" |
| 404 | `ErrBotNotFriend` | "请先把 bot 加为好友再尝试绑定" |
| 429 普通 (`code=429`) | `*ThrottledError` | "请求过于频繁，请 N 秒后再试"；data 里 `retry_after_seconds` 给前端做按钮倒计时 |
| 429 锁定 (`code=4291`) | `*LockedError` | "已锁定，请 30 分钟后再试"；data 里 `retry_after_seconds` |
| 502 | `ErrBotUnavailable` | "系统繁忙，稍后再试" |

前端通过 `api/client.ts::ApiError` 把 envelope 的 `code` + `data` 透传到 UI 层，按 code 区分文案"频繁 / 已锁定"。

---

## QQ 认证前端落地

- 入口：`hfut-front/src/screens/ProfileScreen.tsx` "我的"页菜单 "QQ 认证"
- 屏幕：`hfut-front/src/screens/QQBindScreen.tsx`
- API：`hfut-front/src/api/qqBind.ts`
- 一页两态：
  - **未绑**（学校未认证 → 引导先去 SchoolBind；学校 OK → 输入 QQ → 发码 → 输 6 位码 → 确认）
  - **已绑**（显示 QQ 号 → 解绑按钮 → 发解绑码 → 输 6 位码 → 确认）
- 错误处理：
  - 429 普通限流 / 4291 锁定：自动起按钮倒计时 + alert 文案区分"频繁 / 已锁定"
  - 404（bot 不是好友）：alert 引导 "先在 QQ 加机器人为好友"
- 旧 `EditProfileScreen` + `bind_qq/bind_wx/bind_phone` 字段全部删除（废弃自填式联系方式，统一走 QQ 认证一条链）

---

## 加急通路（P3.3）

复用 hfut → bot 的同一条 JWT 通路（共享 `BOT_SERVICE_JWT_SECRET`，`iss=HFUT-Graduation-Project-hfut`），**不引入** `BOT_INTERNAL_API_TOKEN`。

### 触发链路

```
[App] 订单聊天里长按自己发的、未加急的气泡 → 二次确认
   ↓
[hfut] POST /api/v1/orders/:id/messages/:msg_id/urge
   ├── caller 是订单参与方（账号集语义）
   ├── msg.SenderID ∈ caller 账号集；不是 official；未加急
   ├── 限流：同 (order, caller) 5min 内 1 次（Redis SetNX `order_urge_throttle:{order_id}:{caller_id}`）
   ↓
[hfut] resolveRecipientQQ(对方userID) → 直接返回目标 QQ（int64）：
   - 普通账号 + 绑了 QQ child → child.qq_number
   - 孤儿 QQ child（本身就是 QQ 用户）→ user.qq_number
   - 普通账号 + 没绑 QQ → ErrOrderUrgeRecipientNoQQ
   - **不做群里 @ 兜底**——加急是私聊提醒，群发会泄露订单内容且偏离设计语义
   ↓
[hfut → bot] SendPrivate(qq, text) —— "加急提醒：你在订单 #N 收到一条新消息……"
   ├── 失败 → ErrOrderUrgeBotUnavailable（urgent 标记 + 限流锁仍保留，代表"已尝试加急"）
   ↓
[hfut] MarkUrgent —— WHERE urgent=false 条件 update 保证幂等；rows=0 视为并发已加急
   ↓
[hfut] 写一条 MsgType=Official 的订单消息（"已加急提醒对方查看消息（HH:mm）"）
       让对话双方都能看到事件
```

### 错误码

`OrderMessageUrge` 返回 400 / 403 / 404 / 409 / 429（含 `retry_after_seconds`） / 502。

### 前端

- `api/orders.ts` 加 `urgeOrderMessage(orderId, msgId)` 与 `Msg.urgent / urged_at` 字段
- `OrderChatScreen` 长按自己发的、未加急的气泡 → Alert 二次确认 → 调 API 后 `refreshAll` 拉回
- 加急徽章：气泡右上角红色"加急"小标签 + 红色描边 (`bubbleUrgent`)；图片消息和文字消息都支持

---

## 服务调用审计（P3.4）

bot → hfut 服务调用审计：

- 表：`service_token_audit (id, service, jti, method, path, status_code, remote_ip, duration_ms, created_at)`，迁移见 `migrate_service_token_audit.sql`
- middleware：`BotServiceAuth` 在 `ctx.Next()` 后异步起 goroutine 写一行；只记**通过验签**的请求（401 已被 ZapLogger 记过）
- 写失败仅 log warn，不重试也不影响主流程；体量预估 ~150w 行/月，后续视情况加 30 天 cron 清理
