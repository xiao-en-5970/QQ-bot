# bot/phases — 实施分期归档（P0 ~ P3）

> 父：`bot/SKILL.md`。本文件是历史记录性质，给开发者回顾"哪些功能在哪个阶段做的"。Kimi 运行时不需要看。

---

## P0（窗口聚合 + 识别 + 占位 ack，纯 bot 内部）

- ✅ 加 `BOT_AUTO_REPLY_WINDOW_SECONDS` / `BOT_AUTO_REPLY_MAX_WINDOW_SIZE`
- ✅ `read_skill` 路径白名单（只允许 bot/）
- ✅ `handleAutoReply` 加 per-(group, user) 滑动窗口聚合，60s 沉默触发
- ✅ 调 Kimi 识别（系统 prompt 严格判定为业务动作之一才返回结构化 JSON）
- ✅ 不真上架，只在群里 @ 用户回 "[识别测试] 识别到你想上架/提问/下架 XXX"
- ✅ 验证识别准确度

## P1（hfut 后端临时号 + bot 上下架 + 转存 + 去重）

- ✅ hfut 数据库改造：
  - `users` 加 `account_type` / `qq_number` / `parent_user_id` 字段
  - `articles` 加 `status=close`
  - `goods` 加 `negotiable` 字段（面议）
  - `schools` 加 `qq_groups` 字段
- ✅ hfut `/api/v1/bot/*` service 接口 + JWT 鉴权 middleware（HS256，0 维护数据库 token）
- ✅ hfut service 层去重检查（`FindLikelyDuplicates`）；`POST /goods` 命中返回 409 + 已存在 ID
- ✅ bot hfut 客户端封装：UpsertQQChild / PublishGood / OffShelfGood / ListActiveGoods / PublishArticle / CloseArticle / ListOpenQuestions / UploadImage
- ✅ bot 自动回复 verbosity 开关（verbose / normal），生产无感模式
- ✅ 图片转存（NapCat 临时 URL → hfut OSS 永久 URL，单张失败不影响整体）
- ✅ 端到端跑通"识别 → 创建/复用旗下账号 → 去重 + 转存 → 上架"

## P2a（QQ 绑定核心流程）

- ✅ bot 内部 HTTP server（`internal/api/server.go`）：check-friend / send-private / send-group + Bearer token 鉴权
- ✅ hfut bot 反向客户端（`package/botinternal/client.go`）：单例 + 自动 Init + sentinel error
- ✅ hfut 3 个 user 端 API：`/user/qq-bind/request-code`、`/user/qq-bind/confirm`、`/user/qq-unbind`
- ✅ redis 验证码 + 5min TTL + 同 user 限流（SetNX）
- ✅ 严格 1:1 校验（主账号最多 1 个旗下账号，绑前必须先解绑旧的）
- ✅ 学校归属覆盖（挂载时主账号 school_id 强覆盖旗下账号）
- ✅ 解绑保留数据（parent_user_id 设回 NULL，旗下账号变孤儿，所有商品/提问保留）

## P2b（鉴权改造 + 数据聚合 + 接收人重定向）

**写权限/读列表合并（账号集模型）**：
- ✅ `service.GetAccountIDsForOps` —— 所有"我的"权限校验/列表查询都基于账号集
- ✅ 商品（`UpdateGood/Publish/OffShelf/UploadImages`）+ 文章（`UpdateArticle/Delete/UploadImages/PublishDraft`）owner 校验放宽到"账号集"
- ✅ `resolveOrderParticipant` 改账号集判定——所有订单状态机操作一次性适配
- ✅ "我的商品/提问/草稿/订单/通知" 列表 caller 看自己时聚合主账号+旗下号

**接收人重定向（旗下号是"发布渠道标签"）**：
- ✅ `service.ResolveTargetUserID` —— 非孤儿旗下号 → parent
- ✅ `notification.emit/emitAggregatedLike` 入库前重定向：所有点赞/评论/回复/官方通知自动落到主账号身上

**展示契约**：
- ✅ `/user/info` 返回 `qq_child_user_id` + `qq_child_qq_number`
- ✅ `AuthorProfile` 加 `from_user_id` + `from_username`，前端拼 "username（来自用户 xxx）"
- ✅ `notificationAuthor` 同步带 `from_user_id` + `from_username`（通知发起方是 QQ 旗下号时也展示主账号身份）
- ✅ 前端 **`utils/authorName.ts::formatAuthorName(author)`** 统一拼接：商品列表 / 商品详情 / 文章 / 求助 / 帖子 / 问题 / 回答 / 评论 / 回复 / 通知 / 本地 push 通知 全部走这一个工具，避免各页面拼字串口径漂移

**QQ 认证前端落地**（详见 `qq-bind.md` 末尾段）：
- ✅ "我的"页菜单 "QQ 认证" 入口 → `screens/QQBindScreen.tsx`
- ✅ 两态 UI：未绑（学校未认证 → 引导；学校 OK → 输入 QQ → 发码 → 输 6 位 → 确认）/ 已绑（显示 → 解绑流程）
- ✅ `api/client.ts::ApiError` 把后端 envelope 的 `code` + `data` 透传到前端
- ✅ 旧 `EditProfileScreen` + `bind_qq/bind_wx/bind_phone` 字段全部删除

bot 权限**严格收窄**到 5 类发布动作，所有 app 内交互（评论/点赞/聊天）都走 app，不走 QQ。

## P2c（孤儿账号回复转发 + 请求下架）

由于 P2b 的接收人重定向，**绑定了主账号的旗下号** 已经不需要 P2c：app 用户回复 → 通知主账号 → 主账号在 app 直接处理。P2c 只剩**孤儿旗下号**特殊场景（详见 `orphan.md`）：

- ✅ **创建群持久化**：`users` 加 `created_in_group_id` 字段（迁移：`migrate_qq_child_orphan_group.sql`）；`BotUpsertQQChild` 创建旗下号时填，`first-seen` 群作为孤儿转发回的目标群
- ✅ **孤儿 inbound 通知转发**：`notification.dispatchInbound` 用 `ResolveInboundTarget` 4 路分流
- ✅ **孤儿商品 VO**：GET /goods/:id 及列表条目在 owner 是孤儿时返回 `is_orphan_owner: true` + `seller_qq_number`
- ✅ **孤儿商品前端切换**：`hfut-front/src/screens/GoodDetailScreen.tsx` 检测到 `is_orphan_owner=true` → 隐藏"我想要"按钮，改为"通过 QQ 联系：QQ-XXX"告示 + "请求下架"按钮
- ✅ **请求下架**：`POST /api/v1/goods/:id/request-off-shelf` —— bot 在原群里 @ 卖家"是不是已出？回'是'就下架"。失败时清限流锁让用户能重试；同 (caller, good) 1h 内只能请求一次防刷
- 卖家在 QQ 群里回 "是 / 已出 / 鞋架已出" 等 → 走现有 `off_shelf` 识别链路自动下架（不需要新逻辑）

### P2c 边界 & 回退

- **存量孤儿没 `created_in_group_id`**：迁移前的孤儿 inbound 通知会被静默 drop + log info（不报错）
- **bot 服务不可达**：转发失败仅 log warn，主接口（评论/点赞）仍然成功
- **请求下架 1h 内同一 caller 同一 good 只允许 1 次**：防 app 用户骚扰卖家
- 孤儿绑回主账号后历史 inbound 自动正常

---

## P3（精度优化 + 边缘）

### P3.1 前端文案精简

口径：用户端少看到长句说明，单条 banner / hint 控制在 ~10~14 字以内；不出现"兜底声明""请...后..."这种工具感强的措辞。

实际改动：

- `screens/GoodListScreen.tsx`
  - "右上角选择参考位置后可显示距离" → "选定参考点后显示距离"
  - "商品无坐标时无法算距" → "位置待定"
  - "用来算商品与你的距离。可从地址簿选一条，或用当前定位。" → "选一个参考点，用来算商品距离"
  - "还没有保存的地址，可在下方管理地址后添加" → "还没有保存的地址，去下方添加"
- `screens/OrderChatScreen.tsx` banner 整体收紧
  - 待付款（有 QR）：标题 "待付款" → "获取收款码"；副文案 → "通过收款码付款后可进行下一步"
  - 待付款（无 QR）：副文案 → "卖家暂未提供收款码，可在聊天里商定"
  - 求助 isSeller / isBuyer 多状态副文案精简
- `components/PaymentQrModal.tsx`
  - emptyTitle "卖家未提供收款码" → "暂无收款码"
  - hint "转账后把付款截图发到聊天作为凭证" → "付款后发截图到聊天作为凭证"
  - 保存成功 Alert 副文案 "打开相册即可使用收款码付款" → "可在相册中打开使用"
- `components/CheckoutAddressModal.tsx`
  - 默认 hint "聊天与订单绑定；创建订单后卖方可在订单中查看收货位置与距离。" → "下单后卖方可看到你的收货位置与距离"
- `screens/GoodCreateScreen.tsx`
  - 收款码 hint 两行合并 → "上传后买家可在订单中查看；留空时由你和买家在聊天里商定"

未来风格：**标题动词化**、**副文案 ≤14 汉字**、**不出现"请"开头的祈使长句**、**避免"将...转..."这种 RPC 化口吻**。

### P3.2 多在售时反问消歧

详见 `recognition.md` "已出/已找到 消歧" 段。

### P3.3 QQ 加急

详见 `qq-bind.md` "加急通路" 段。

### P3.4 限流 / 错误锁定 / 审计日志

- bot → hfut 服务调用审计：详见 `qq-bind.md` "服务调用审计" 段
- QQ 绑定 / 解绑错码锁：详见 `qq-bind.md` "限流 / 错码锁" 段
- bot dispatch 限流：详见 `recognition.md` "bot dispatch 限流" 段

### P3.5 Kimi prompt 再调优

详见 `recognition.md` "Kimi prompt hard rules" 段。

### P3.6 模型可配置 + quota 熔断 + regex 兜底识别

观测到生产日志全是 `exceeded_current_quota_error`，bot 41 小时一次都没成功调过 Kimi。修复：

- ✅ **模型抽 conf**：`Gpt.Model` (env `GPT_MODEL`) + `Gpt.RecognizeModel` (env `GPT_RECOGNIZE_MODEL`)；写死的 `ModelMoonshotV1128K` 改读 conf。默认升级：闲聊用 `moonshot-v1-auto`（按上下文省钱）、识别用 `kimi-k2-0905-preview`（中文识别 + JSON 输出更稳）。go-moonshot SDK 的 `ChatCompletionsModelID` 是 string 别名，可传任意 Moonshot 服务端支持的模型名
- ✅ **quotaGate 熔断器**：`utils/kimi/quota_gate.go`；连续 ≥ `GPT_QUOTA_ERROR_THRESHOLD`（默认 3）次 quota 错 → 冷却 `GPT_QUOTA_COOLDOWN_SECONDS`（默认 1800）秒，期间 LLM 入口 short-circuit 不再撞 API
- ✅ **regex 兜底识别**：`utils/kimi/recognize_regex.go`；熔断期间 `auto_reply.go` 退化为正则识别 publish_good + off_shelf；保守严格（仅锚定句式 + 价格强制 + 撤回关键词 hard reject + 价格上限保护）
- ✅ **ack 文案区分**：regex 兜底产出的 ack 用 "已（关键词识别）上架..." 让用户感知到不是 LLM 识别，附"如不对请回'撤销'"
- ✅ 单元测试：26 个 case 覆盖 quota gate 状态机 (open/trip/recover/reset) + regex 各类命中 / 拒判 / 边界

详见 `recognition.md` "LLM 配额熔断 + regex 兜底识别（P3.6）" 段。

### 其他

- 重启窗口持久化（用了一段时间觉得"丢一半窗口"难受再做；目前接受丢）
- OSS 历史镜像清理：cron 任务清理 30 天前的 `user/*/bot/img_*` 文件（同 service_token_audit 一并 30 天 cron 化）
