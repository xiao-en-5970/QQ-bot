# bot — bot 自身的能力 / 行为约束 / 自动回复策略

> 父 skill：`../SKILL.md`（顶层索引）。

> 这个 skill 不描述 HTTP API，而是描述 **bot 自己怎么响应群消息、什么时候该说话、什么时候该闭嘴**。Kimi 在群里被触发时应当先 `read_skill(path="bot/SKILL.md")` 拿到本文件，再决定怎么回应。

> ⚠️ **重要约束**：bot 运行时**只能读 `bot/` 子树和最顶层 `SKILL.md`**，**禁止读 `hfut-union/` 子 skill**。`hfut-union/` 那一套 API 文档是给开发者写代码时参考的，权限太高、不应该让 Kimi 在跟用户对话时直接拼 HTTP 调用——业务能力必须经过下面"工具集"那一层封装才能用。`read_skill` 工具会强制路径白名单。

---

## bot 是什么

接到 NapCat WebSocket 推送的群消息事件，识别两类触发：

1. **@bot 命令路径**：`@bot jm 350234` / `@bot chat 你好` 等显式指令
2. **白名单群自动监听路径**：用户在群里说"出鞋架 6 元"、"代课 30r"、"已出"等业务句子，bot 自动识别 → 在 hfut-union app 里同步上下架 / 提问 / 回答

## 触发模式 1：@bot 命令路径

用户消息以 `@bot` 开头（segment[0] 是 at 且 qq=botID）→ 走显式命令分发：

| 命令 | 行为 |
|---|---|
| `@bot jm 350234` | JM 本子下载 |
| `@bot pix neko` | pixiv 图 |
| `@bot chat 你好` | Kimi 聊天 |
| `@bot help` | 显示菜单 |
| `@bot github` | 项目链接 |
| `@bot 你好`（关键字未匹配） | 走 `commands.default` 兜底命令（一般是 chat）|

每个子命令是否启用受 `commands.enabled` 白名单控制。入口代码：`logic/handle_at_message.go::handleAtCommand`。

## 触发模式 2：群聊白名单自动监听路径

群号在 `conf.Cfg.Group.AutoReplyWhitelist` 里 → 抓取所有非 bot 自己发的群消息（不需要 @bot），由 LLM 判断是不是 5 类业务动作之一并自动同步到 hfut-union app。

入口代码：`logic/handle_at_message.go::handleAutoReply`。

---

## 业务动作识别（核心策略）

### 5 类业务动作

| 动作 | 群里典型句式 | 同步到 hfut |
|---|---|---|
| **A. 上架二手** | "出三层鞋架 6元 [图片]"、"出按压U型枕 5元" | `goods` (category=1)，价格不填 → 面议 |
| **B. 上架有偿求助 / AA 活动 / 拼团** | "5.7 早十代课 30r"、"求人帮带饭"、"出去玩 人均 20r"、"拼车去机场 AA 30" | `goods` (category=2)；这条覆盖范围比字面"有偿求助"宽，前端文案以后改成"求助/活动" |
| **C. 上架提问** | "有人有形势与政策8的题库吗"（向整个群提问） | `articles` (type=2 question) |
| **D. 提交回答** | 别人提问后某用户精准答了 | `articles` (type=3 answer)，挂到原 question 下 |
| **E. 下架/已找** | "鞋架已出"、"已找到" / "已出"（不指明） | `POST /goods/:id/off-shelf` 或 `articles.status=close` |

### 窗口聚合（解决"图 + 文跨多条"问题）

群里一个上架动作经常跨多条消息：

```
姜糖可乐 22:59:21  [图片]
姜糖可乐 22:59:29  出三层鞋架 6元
姜糖可乐 22:59:43  [图片]
姜糖可乐 22:59:43  出按压U型枕 5元
```

bot 必须把第 1 张图绑给"鞋架"、第 3 张图绑给"U型枕"。规则：

- **per-(group, user) 滑动窗口**——每个 (群, 发送者) 各自一个独立桶
- **沉默 60s 触发**（可配置，env `BOT_AUTO_REPLY_WINDOW_SECONDS`，默认 60）
- 同桶内消息合并成一段上下文，整段交给 Kimi
- Kimi 一次返回 N 个动作（可能 0、可能 多个）
- 多个发送者交错时**严格按发送者分桶**，不混

### 准确度优先（宁可漏，不可错）

- 模糊判定一律 drop（不打扰用户）
- 真触发上架时**必须 @ 用户确认**（"@用户 已为你上架：三层鞋架 6元"）
- **不需要管理员预览**——目标是"用户群里发一句无感同步到 app"

### "已出/已找到" 消歧

- 该用户当前**只有 1 个在售商品** → 直接下架
- 多个在售 → bot @ 用户反问"你最近在挂'三层鞋架'和'按压U型枕'，下架哪个？"
- 反问后用户的回复也进窗口，Kimi 收到完整上下文再给最终决定

### 提问 vs 商品的判别

- 提问句式典型："有人有 XX 吗"、"问下大家 XX"、"求 XX"（求资源、求经验，不带价格）
- 商品句式典型："出 XX N元"、"卖 XX"、"求人 XX 30r"（带价格 / 带"代"字）
- 模糊时优先判**不上架**（保守）

### 提问的 status=close

- hfut article 表加 `status=close` 表示提问被关闭、不接受回答
- 用户**主动**在前端关闭，bot 不会自己关闭
- bot 提交回答前要先查这个 question 是否 status=close，是则 drop

---

## 工具集（Kimi 在自动监听路径下的全部能力）

> Kimi 不能直接拼 hfut HTTP 调用。所有跟 hfut 的交互必须走下面这些封装好的工具。**P0 阶段这些工具还没实现**——当前先识别 + log，等 hfut 后端改造完成后再接通。

### 现已实现

- `read_skill(path)` — 读 bot 自身 skill 文档。**路径白名单：只允许 `bot/**` 和最顶层 `SKILL.md`**；尝试读 `hfut-union/**` 会被拒绝并返回错误

### P1 阶段实现（hfut 后端 + bot 客户端配合）

- `publish_good(qq_number, title, description, price?, category, location?, image_urls)` — 上架商品；price 不传 = 面议
- `off_shelf_good(qq_number, good_id?)` — 下架；不传 good_id 时自动选该用户唯一在售（多个则 LLM 应该先调 `list_my_active_goods`）
- `list_my_active_goods(qq_number)` — 查该用户当前在售商品（消歧用）
- `publish_question(qq_number, title, content, image_urls)` — 创建提问
- `publish_answer(qq_number, parent_question_id, content)` — 提交回答
- `list_recent_open_questions(group_id)` — 查群内最近未关闭的提问（定位 parent_question_id）
- `reply_in_group(group_id, at_qq, text)` — bot 在群里 @ 用户回执（"已为你上架 XXX"）

### 工具调用规则

- **禁止**：让 Kimi 直接生成 / 推荐 hfut HTTP URL
- **禁止**：让 Kimi 跨用户操作（操作 A 用户的商品时不能传 B 的 qq_number；下层鉴权也会兜底）
- **建议**：先调"查询类"工具确认状态（list_my_active_goods / list_recent_open_questions）再调"写入类"工具

### 商品去重（避免重复上架）

同一发布者在 QQ 群里反复发**参数高度相似**的商品（典型场景：用户先发"出鞋架 6元"，过几个小时又发了一遍来顶帖，或者隔几天补图重发），bot 不能重复创建——会让 hfut 列表被同款商品污染。

**bot 层去重（P1 必须做）**：
1. `publish_good` 工具调用前**先调** `list_my_active_goods(qq_number)` 拿该用户当前在售清单
2. 把候选项一起喂给 LLM，让模型判断"我现在想发的这条，跟列表里某条是不是几乎一样？"
3. 是 → 不调 `publish_good`，群里 @ 用户回："你这条像之前发的'XXX'，没重复上架（如要重发请先 @bot 下架旧的）"
4. 否 → 走正常 publish_good 流程

**hfut 后端兜底去重（P1 必须做）**：
- service 层加 `IsLikelyDuplicate(user_id, category, title, price)` 检查
- 判定标准：同一 user + 同一 category + 标题相似度 ≥ 0.8（ngram / Jaccard）+ 价格差 ≤ 0.1 倍 + 7 天内
- 命中：`POST /goods` 返回 409 + 已存在的商品 ID，让 bot 知道"已存在"
- 即使 bot 因为模型幻觉漏判，后端也兜底拒绝创建——避免任何路径下污染数据
- **管理员 / 主账号通过 app 上架不受此限制**（人为决定就允许重发）

**误判保险**：
- 如果用户明确说"我重新发一遍 XX 因为图模糊了" → bot 应该先调 `off_shelf` 把旧的下了，再调 `publish_good`，绕开去重
- bot 层 LLM 判断不准时，hfut 兜底返回 409，bot 收到 409 后**不要慌张**——直接群里 @ 用户：「你这条跟之前的'XXX'撞了，要重发请先回'下架旧的'」

### 图片转存（NapCat 临时 URL → hfut OSS 永久 URL）

NapCat 在 `image` segment 里给的 URL 是腾讯多媒体的临时签名链接，**几天后会失效**。如果 bot 直接把这个 URL 入 hfut goods.images，过几天用户在 app 看商品就只剩死链。

**链路**（P1.4b 实现）：
1. dispatch 拿到 RecognizeAction 后，先调 `imageURLsFromSnap` 找出 NapCat 临时 URL
2. 调 `mirrorImagesToHfut(ctx, userID, urls)`：每张图独立走"GET NapCat URL → multipart POST 到 hfut → 拿永久 URL"
3. 把转存后的永久 URL 列表传给 `PublishGood` / `PublishArticle` 入库

**hfut 端接口**：`POST /api/v1/bot/images` (multipart/form-data)
- 字段：`file`（二进制）+ `user_id`（int）
- 走 `BotServiceAuth` 中间件，跟其他 bot 接口一致
- 存到 OSS 路径 `user/{user_id}/bot/img_{snowflake}.{ext}`——用 user/ 前缀的"用户级图床"避开"good_id 还没建"的鸡生蛋问题
- 限制：单张 ≤ 10MB；扩展名白名单 jpg/jpeg/png/gif/webp
- 返回 `{ url: "<完整可达的 OSS URL>" }`

**失败处理**（关键设计）：
- **每张图独立**：循环里 try-catch 每张图的下载+上传，**任一张失败仅 skip 那张** + log warning，不阻塞其它图
- **下载超时 15s / 上传超时 30s**：保证 dispatch 整体不会因为图片网络抖动卡死
- **大小预检**：`io.LimitReader` 截断，超过 10MB 直接拒收，防 OOM
- **0 张成功也允许商品发布**：`goods.images=[]` 比"商品创建失败"友好——商品至少是真实的

**清理策略**（暂未实现）：
- `user/{id}/bot/img_*` 路径下的图随 good 创建/删除不级联清理（路径里没有 good_id 关联）
- 长期会堆积；P3 阶段加 cron 任务"清理 30 天前的 user/*/bot/img_* 文件"

---

## 临时账号 / 旗下账号 概念（hfut 后端配套设计，bot 间接依赖）

### 旗下账号 ≠ 真账号——它是"发布渠道标签"

**核心设计哲学（P2b 阶段澄清）**：QQ 旗下账号**不是真正的账号**，而是给"通过 QQ 渠道发布"打的标签。它持有：
- 自己发布过的资源（goods.user_id / articles.user_id 指向旗下号 ID）——保留"是谁发的"语义
- 自己的学校归属（旗下号能不能在某学校群里发东西的依据）

它**不持有**：
- inbox / 通知（`notification.user_id` 重定向到主账号）
- 对话 / 私信（私信功能未实现；将来如有，接收人也直接是主账号）
- 任何"被动接收"语义

```
主账号 (account_type=normal, parent_user_id=null)
  └── QQ 旗下账号 (account_type=qq_child, parent_user_id=主账号ID, qq_number=xxx)
       ↑ 仅作"发布身份"，所有 inbound 重定向到 parent
```

**关键约束**：
- QQ 旗下账号**永远不能独立登录**（`password` 空 / 标记 inactive）
- 用户名 `qq{qq号}`，昵称 `【QQ】{QQ群名片}`（实时跟群名片同步）
- **严格 1:1**：一个主账号最多 1 个 QQ 旗下账号；要绑第 2 个 QQ 必须先解绑当前 QQ
- bot 运行时所有写操作都是以 QQ 旗下账号身份做的（`user_id` 是子账号 ID）

### bot 的权限边界（**严格收窄**）

bot 只负责"通过 QQ 触发的发布操作"——5 种：

| 动作 | 类型 |
|---|---|
| `publish_good` | 上架商品（旗下号身份） |
| `publish_question` | 发提问（旗下号身份） |
| `publish_answer` | 写回答（旗下号身份） |
| `off_shelf` | 下架自己挂的商品（"已售/已找到"语义） |
| `close_question` | 关闭自己挂的提问（"问题已解决"语义） |

bot **不**负责的事：
- ❌ 评论别人 / 点赞 / 收藏：纯 app 内交互，不该由 QQ 触发
- ❌ 主动给别人发 QQ 私聊：只在群消息触发时回执（防 abuse）
- ❌ 在群里"代主账号回复"：app 内交互不重新出口到 QQ
- ❌ 跨用户操作他人资源：旗下号只能管自己挂的商品/提问，bot 工具集严格按调用方鉴权

### 接收人重定向（核心机制，P2b 实施）

任何"指向某 user_id 的被动接收"都通过 `service.ResolveTargetUserID(target)` 做转换：

| target | 重定向后 |
|---|---|
| 普通账号 | 不变 |
| 非孤儿 QQ 旗下号 | → `parent_user_id`（主账号） |
| 孤儿 QQ 旗下号 | 不变（保持指向孤儿；P2c 阶段 bot 转发回 QQ 群） |
| 不存在 / 已禁用 | 不变（让上层校验拦掉） |

应用点（已实现）：
- `service.notificationService.emit / emitAggregatedLike` —— **单点改造覆盖全部 4 类通知**：点赞文章 / 点赞评论 / 顶层评论 / 回复评论。所有通知 `user_id`（接收人）入库前自动重定向。
- 商品 / 文章的 owner 资源校验：通过 P2b 的"账号集"模型解决（caller 是主账号时能动旗下号的资源）。

效果：
- 别人评论旗下号的提问 → 主账号在 app 通知里看到"X 评论了你的提问"（不需要查询时聚合）
- 别人点赞旗下号的商品 → 主账号在 app 收到通知
- 主账号在 app 里点击通知 → 进入提问/商品页 → 用主账号身份回复（不再涉及旗下号）

### 作者展示：username（来自用户 xxx）

`vo.AuthorProfile` 新增 `from_user_id` + `from_username`——非孤儿 QQ 旗下号作为作者时填充主账号信息。前端按需拼成形如：

> **【QQ】小张**（来自用户 _xiao_zhang_）

让别人看到这条内容是**主账号 xiao_zhang 通过 QQ 渠道发的**，身份不会丢失。已应用到：文章列表 / 单篇 / 评论 / 商品 4 处 enrich。

孤儿旗下号没有 from_*——前端展示就是干净的"【QQ】小张"，无主账号关联。

### 创建 / 挂载 / 解绑时机

| 触发 | 行为 |
|---|---|
| **QQ 用户首次在白名单群里被 bot 识别**且该 QQ 还没旗下账号 | 创建一个**孤儿旗下账号**（`parent_user_id=null`），用 QQ 群名片作为 nickname、`qq_number` 唯一索引 |
| **用户 app 主动绑 QQ X**（前置：主账号已绑学校） | 查 X 是否有孤儿旗下账号 → 有则 `parent_user_id` 设为主账号 ID + 学校信息覆盖；无则**创建一个空旗下账号**直接挂上 |
| **用户主动解绑 QQ** | 旗下账号 `parent_user_id` 设回 null（变孤儿），保留所有数据 |
| **同一主账号要绑别的 QQ** | **必须先解绑当前 QQ**（严格 1:1，没有"切换"快捷方式，强制经过解绑步骤） |
| **某个 QQ 之前是孤儿、被另一个主账号绑了** | 直接挂到新主账号上；原主账号要找回这个 QQ 的话只能从新主账号那边解绑后再绑回来 |

### 学校归属（**严格的 > 不严格的**）

学校信息有两种来源，可信度不一样：

| 来源 | 可信度 | 路径 |
|---|---|---|
| **严格**：主账号自己走过学校 CAS 认证（user.bind/school） | 高，本人证明 | 用户在 app 里主动绑学校 |
| **不严格**：旗下账号通过 QQ 群-学校映射推断 | 低，可能有跨校群成员 | bot 创建临时号时按 `schools.qq_groups` 推断 |

> ⚠️ **业务前置**：用户必须**先绑学校再绑 QQ**——绑学校是 P1 阶段的现成功能（`POST /user/bind/school`，CAS 认证），绑 QQ 是 P2 新加的。绑 QQ 流程入口要求主账号已经有 school_id。

合并规则：

- 旗下账号挂到主账号时，**用主账号的严格学校信息直接覆盖旗下账号的学校信息**（不严格的让位于严格的）
- 不用提示用户、不用让用户选——身份认证以"本人 CAS"为准，旗下账号是辅助
- 用一个边界场景验证：主账号是合工大（CAS 认证过），绑了一个工程大的群创建的 QQ 旗下号 → 挂载后旗下号学校改为合工大，原工程大的痕迹丢弃

旗下账号的能力约束（学校层面）：

- 旗下账号本质上是"不严格"的身份，所以它能做的事被刻意限制（仅 bot 触发的写操作、不能登录、聊天受限等——见下文"孤儿账号特殊行为"）
- 旗下账号**只能在自身学校支持的群里**通过 bot 上架——主账号是合工大，那这个旗下号只在合工大白名单的群里有效，跨学校的群即便消息能进来也会被 bot 工具集鉴权拒绝

### 数据聚合 / 操作权限（P2b 已实现）

两个核心抽象：

1. **`service.GetAccountIDsForOps(callerID) → AccountIDSet{Caller, ChildID, AllIDs}`**
   caller 在做"我的"操作时能 access 的所有 user_id 集合（= 主账号 + 旗下账号）。
   用于：列表查询合并 + 资源 owner 校验放宽到"账号集"。

2. **`service.ResolveTargetUserID(targetUserID) → uint`**
   接收人重定向：非孤儿旗下号 → parent_user_id；其它原样。
   用于：通知 emit 入库前——所有 inbound 落到主账号身上。

应用：

- **列表合并展示**："我的商品" / "我的提问/帖子/回答" / "我的草稿" / "我的买卖订单" / "我的通知" 等列表，caller 看自己时把主账号 + 旗下号的内容**合并按时间倒序**返回
- **写权限对称**：主账号能改/删/上下架/确认收款发货 caller 集合里**任一账号**的资源（商品/文章/订单状态机的 owner 校验全部走 `IsOwnedByOneOf`）
- **接收人重定向**：通知（点赞/评论/回复/官方）入库前重定向，DB 里 `user_id` 直接是主账号 id——查询不需要再聚合，自动正确
- **作者展示带"来自用户 xxx"**：`AuthorProfile.from_user_id/from_username` 让前端拼"username（来自用户 xxx）"
- **数据聚合契约**：`/user/info` 响应里返回 `qq_child_user_id` + `qq_child_qq_number`
- **看别人 vs 看自己**：看别人的列表时**不**聚合（看不到别人的旗下号资源），看自己时才合并
- 不允许主账号操控 bot 主动发 QQ 消息（防 abuse；bot 自己只在群消息触发时回执）

#### "账号集"权限模型的安全保险

- caller 自己是旗下账号（理论上 password 空登录不进来）→ `GetAccountIDsForOps` 只返回它自己，**不**递归向上找 parent——即便 admin 直接 SQL 改密码让旗下号能登录，攻击者也拿不到 parent 的资源
- caller 没绑 QQ 旗下账号 → 集合只有 caller 本人，所有 IN 查询退化为单 user_id 等值，行为跟改造前一致

#### 防"账号集内自交易"（订单）

主账号在 app 给**自己旗下号**挂的商品下单 = 自己跟自己交易，会刷出虚假订单数 / 评分。
`service.isSelfTrade(callerID, sellerID)` 用账号集判断："caller 自己" 或 "caller 的旗下号" 是 seller 都拒绝。已应用到：
- `Create`（普通商品下单）
- `createHelpOrder`（有偿求助接单）
- `createDraft`（"我想要"创建会话草稿）

通知层（点赞 / 评论自己内容）：`emit` 入库前 dedupe `from_user_id == user_id`，且重定向后再 dedupe 一次—— "主账号 X 点赞自己旗下号商品" 静默跳过，不发通知、不计数。
- **"QQ 加急"功能**（P3 排期）：商品聊天界面**长按聊天气泡 → 弹出菜单 → 选"加急" → bot 把这条聊天发给对方 QQ 私聊**。
  - 前端 UI：被加急的气泡渲染成**红色** + 标注"加急"标识（让发送人和接收人都看到）
  - 后端：长按"加急"后调 hfut 接口（如 `POST /api/v1/orders/:id/messages/:msg_id/urge`），hfut 反查接收人 → 调 bot 内部 API → bot 发 QQ 私聊
  - 限流：同一聊天 N 分钟内最多加急 1 次（防滥用）；接收人未绑 QQ 时拒绝
  - 数据：`order_messages` 表加 `urgent BOOL DEFAULT false` + `urged_at TIMESTAMP NULL`（详见 P3 实施时再确定）

### 孤儿旗下账号的特殊行为

"孤儿"= 旗下账号的 `parent_user_id` 为 null（之前被解绑、或还没人绑过）。**孤儿账号在 app 里仍然可见**（它发布的商品 / 问答还存在），但功能严重受限——因为没有"在线人"对应：

#### 孤儿的问答收到 app 内回复 → bot 转发回当年的群

```
孤儿账号 child-A（在 QQ 群 G 里创建过、问过"有人有形势与政策题库吗"）
  ↓ 别人在 app 里给这条问答回复："我有，私聊我"（app 里的回答者是真实主账号 U）
  ↓
[hfut] 检测到回复目标是孤儿账号 → 调 bot：转发到群 G
  ↓
[bot] 在群 G 里发："【来自 app 用户 U】对你那条提问'XX'的回答：我有，私聊我"
```

注意：转发用的是"标注来自哪个 app 用户"的格式，不冒充 app 用户身份；让群里那个原始 QQ 用户能看到、自行决定要不要去加 app 联系。

#### 孤儿的商品被 app 用户想买 → 不开放聊天，只挂告示

孤儿账号挂的商品有"联系卖家"按钮，但点开后**不进入聊天窗口**，而是显示一段告示：

```
该卖家尚未绑定 app 账号，可通过 QQ 联系：QQ-12345678
（如该商品已经卖出，请点击"请求下架"）
```

留 "请求下架" 按钮——但**点了不立即下架**，而是触发 bot 在原群里 @ 该 QQ："你的商品 XX 是不是已出？回'是'就下架"。这是**手动审计**的兜底，避免 app 用户恶意点别人的"已出"。

也可以做成"app 用户点了'请求下架'就直接下架"，简单粗暴；但建议先走"群里确认"流程，看有没有滥用再 P3 调整。

#### 孤儿账号绑回主账号后

旗下号被某个主账号绑定（无论是原 owner 还是新主账号绑的同一 QQ）→ 孤儿状态消失：

- 商品的"联系卖家"恢复正常聊天窗口（聊天对端是新挂上来的主账号）
- 问答的回复正常进 app 通知，不再转发回群
- 主账号能在自己的聊天页 / "进入你的 QQ 智能体" 受限界面里看到所有相关聊天
- 之前 app 用户在孤儿期发的回复、留言要做**保留并可见**（不要丢，这是用户的合理预期）

---

## 绑定 QQ 流程（hfut + bot 互调）

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

### 实现状态（P2a 已完成）

#### bot 端：反向 HTTP API server（`internal/api/server.go`）

- 端口：`BOT_INTERNAL_API_PORT`（默认 8090）；docker 部署**不要 ports 映射到宿主机**，仅 internal network 暴露
- 鉴权：服务间 JWT，跟 bot → hfut 方向**对称**——
  - 共享 HS256 secret = `HFUT_API_JWT_SECRET`（bot 端 env） / `BOT_SERVICE_JWT_SECRET`（hfut 端 env），同一个值；
  - hfut 每次自签 60s 有效期 JWT，放 `X-Service-Token` 头；
  - iss = `HFUT-Graduation-Project-hfut`（跟反向 `HFUT-Graduation-Project-bot` 不同——防止两个方向的 token 互换使用）；
  - secret 空时 server 不启动（安全降级）。
- 路由：
  - `POST /internal/qq/check-friend` body `{qq_number, no_cache?}` → `{is_friend: bool}`
  - `POST /internal/qq/send-private` body `{qq_number, text}` → `{message_id}`
  - `POST /internal/qq/send-group`   body `{group_id, qq_number?, text}` → `{status: "ok"}`（qq_number=0 时发纯文本群消息，否则 @ 该用户）
  - `GET  /internal/healthz`（不需要 token，给 docker compose healthcheck 用）

#### hfut 端：4 个 RESTful 端点（`controller/qq_bind.go`，走 user JWT）

绑定 / 解绑**都走 request-code → confirm 两步**，对称设计——解绑也要 QQ 端能收到验证码才能成功，防主账号 token 被盗后攻击者"解绑别人 + 自己重新绑 + 盗取旗下账号数据"的攻击向量。

- `POST /api/v1/user/qq-bind/request-code` body `{qq_number}` → `{ttl_seconds: 300}`（hfut 调 bot CheckFriend → 是好友再发码）
- `POST /api/v1/user/qq-bind/confirm`      body `{qq_number, code}` → 200/400
- `POST /api/v1/user/qq-unbind/request-code` 无 body（自动取当前绑定的 QQ）→ `{ttl_seconds: 300}`
- `POST /api/v1/user/qq-unbind/confirm`      body `{code}` → 200/400

校验顺序（每个写入端点都走）：
1. 主账号是 `account_type=normal` 且 `school_id != 0`（先绑学校再绑 QQ）
2. 当前主账号下没有挂着的旗下账号（严格 1:1）
3. 验证码逻辑：

请求验证码时调用 bot.CheckFriend；不是好友 → 404；是 → redis 存 `qq_bind_code:{qq}=={code, requesting_user_id, created_at}` TTL 5min，再调 bot.SendPrivate 发到 QQ。

确认时校验 redis：code + requesting_user_id 匹配；命中 → tx 内挂载（找现有孤儿旗下账号 → 设 parent_user_id + 用主账号 school_id 覆盖；找不到 → 创建空旗下账号直接挂上）。tx commit 后删 redis code。

解绑：跟绑定对称的两步走——`request-code` 阶段从主账号查出当前绑定的 QQ，调 bot 给那个 QQ 发"解绑确认验证码"私聊；`confirm` 阶段校验 code 后真把 `parent_user_id` 设回 NULL，**不删任何数据**——旗下账号的商品 / 提问继续存在但变孤儿。

解绑文案故意区别于绑定（"您正在**解除**当前 QQ 与 app 账号的绑定" + "如非本人操作请忽略此消息——可能是你的 app 账号被盗"），让用户清楚操作意图、能识别异常。

解绑限流跟绑定**独立**（不同 redis key 前缀 `qq_unbind_throttle:` / `qq_bind_throttle:`）——避免用户绑定刚撞限流就也走不了解绑这种死锁。

#### 成功通知（在 confirm 完之后再发一次私聊）

绑定 / 解绑 confirm 成功之后，bot 都会**再发一次私聊确认通知**——让用户感知账号变更，发现异常时立刻反应：

| 时机 | 私聊文案 |
|---|---|
| 绑定成功 | "绑定成功 ✅ 当前 QQ XXX 已成功绑定到 app 账号 \"username\"。如非本人操作，请立即在 app 里解绑并修改密码。" |
| 解绑成功 | "解绑成功 ✅ QQ XXX 已与 app 账号 \"username\" 解除绑定，旗下账号变成孤儿状态（商品/提问数据保留）。如非本人操作，请立即修改 app 密码并重新绑定。" |

实现要点：
- 通知**异步**于父 ctx 发出（独立 30s timeout），前端接口已经响应也会继续发
- 通知失败仅 log warn，**不回滚** DB——绑定结果以事务为准，通知是辅助
- 这是一种"账号安全提醒"——参考银行卡变更短信通知的设计，让用户即便被盗号也能从 QQ 端发现

### 限流

- 同一 user 60 秒内最多请求 1 次验证码（redis SetNX `qq_bind_throttle:{user_id}` TTL 60s）
  跟前端"获取验证码"按钮的倒计时对齐——常规体验
- 每次新请求会显式 Del 旧的 `qq_bind_code:{qq}`（同 QQ 的旧验证码立即失效），避免歧义
- 错 5 次 → 锁 30min（**P2a 暂未做**，等观察实际滥用情况再 P3 加）

### 错误回执（前端按需提示）

| 状态码 | sentinel | 用户感知 |
|---|---|---|
| 400 | `ErrQQNumberInvalid` / `ErrUserNotBoundSchool` / `ErrUserAlreadyBoundQQ` | 提示具体原因（"请先绑学校"/"已绑过 QQ"等） |
| 400 | `ErrCodeInvalid` / `ErrCodeExpired` | "验证码错误 / 已过期，请重试" |
| 404 | `ErrBotNotFriend` | "请先把 bot 加为好友再尝试绑定" |
| 429 | `*ThrottledError` | "请求过于频繁，请 N 秒后再试"；data 里 `retry_after_seconds` 给前端做按钮倒计时 |
| 502 | `ErrBotUnavailable` | "系统繁忙，稍后再试" |

---

## 配置速查（env）

| env | 作用 | 默认 |
|---|---|---|
| `GROUP_AUTO_REPLY_WHITELIST` | 启用自动监听的群号（逗号分隔） | 空 |
| `BOT_AUTO_REPLY_WINDOW_SECONDS` | 同一发送者沉默 N 秒后触发窗口 | 60 |
| `BOT_AUTO_REPLY_MAX_WINDOW_SIZE` | 同一窗口最多攒多少条（防超长） | 20 |
| `GROUP_AUTO_REPLY_VERBOSITY` | 自动回复模式：`verbose` / `normal`（详见"无感模式"子章节） | verbose |
| `COMMANDS_ENABLED` | 启用的子命令（白名单） | 空 = 全禁用 |
| `COMMANDS_DEFAULT` | 无前缀兜底命令 | 空 = 显示菜单 |
| `GPT_API_KEY` | Moonshot API Key | 空 = 不启用聊天 |
| `GPT_MAX_TOOL_ROUNDS` | Kimi 单次 Chat 工具往返上限 | 5 |
| `GPT_MAX_CONTEXT_SIZE` | 单用户保留最近 N 轮对话 | 40 |
| `HFUT_API_URL` | hfut 后端 base URL | — |
| `HFUT_API_JWT_SECRET` | bot 跟 hfut 共享的 service-to-service JWT secret（HS256） | — |
| `BOT_INTERNAL_API_PORT` | bot 内部 HTTP server 端口（仅 internal network 监听） | 8090 |

### 无感模式（auto_reply_verbosity）

bot 在自动监听路径下会按"识别动作"输出回执，回执有 5 类等级（`logic.ackKind`）：

| 等级 | 例子 | verbose 模式发？ | normal 模式发？ |
|---|---|---|---|
| `success` | "已为你上架二手「鞋架」：6 元（goods_id=42）" | ✅ | ✅ |
| `dup` | "你最近已经发过类似的「鞋架」（goods_id=42），没有重复上架..." | ✅ | ✅ |
| `ask_user` | "你最近在挂这几件：「A」「B」，请明确说要下架哪一个" | ✅ | ✅ |
| `fail` | "已识别到「X」，但同步到 app 失败了，稍后再试" | ✅ | ❌ 静默 |
| `ignore` | 群没配学校 / 未识别动作（none） | ❌ 静默 | ❌ 静默 |

设计动机：
- **生产环境**用户没主动喊 bot，bot 只在"产生了实际后果"才出声——`success` / `dup` / `ask_user` 都是用户**需要**知道的
- 反过来 `fail`（如 hfut 网络抖一下）在群里抛错没意义，反而让群友迷惑——`normal` 模式悄悄重试 / 完全静默更友好
- **开发调试**开 `verbose` 把所有路径都暴露，方便看 bot 在干啥
- 默认 `verbose`——忘配 / 配错时倾向"开发友好"（看得到日志）而不是"悄无声息"

切换：
```yaml
group:
  auto_reply_verbosity: normal   # 生产推荐
```
或 env：`GROUP_AUTO_REPLY_VERBOSITY=normal`，重启 bot 即生效。

注意：抑制掉的回执仍然在 zap log 里以 `INFO autoReply ack 抑制 ...` 记录，便于审计——不是真的丢了。

---

## 实施分期

### P0（窗口聚合 + 识别 + 占位 ack，纯 bot 内部）

- ✅ 加 `BOT_AUTO_REPLY_WINDOW_SECONDS` / `BOT_AUTO_REPLY_MAX_WINDOW_SIZE`
- ✅ `read_skill` 路径白名单（只允许 bot/）
- ✅ `handleAutoReply` 加 per-(group, user) 滑动窗口聚合，60s 沉默触发
- ✅ 调 Kimi 识别（系统 prompt 严格判定为业务动作之一才返回结构化 JSON）
- ✅ 不真上架，只在群里 @ 用户回 "[识别测试] 识别到你想上架/提问/下架 XXX"
- ✅ 验证识别准确度

### P1（hfut 后端临时号 + bot 上下架 + 转存 + 去重）

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

### P2a（QQ 绑定核心流程）

- ✅ bot 内部 HTTP server（`internal/api/server.go`）：check-friend / send-private / send-group + Bearer token 鉴权
- ✅ hfut bot 反向客户端（`package/botinternal/client.go`）：单例 + 自动 Init + sentinel error
- ✅ hfut 3 个 user 端 API：`/user/qq-bind/request-code`、`/user/qq-bind/confirm`、`/user/qq-unbind`
- ✅ redis 验证码 + 5min TTL + 同 user 限流（SetNX）
- ✅ 严格 1:1 校验（主账号最多 1 个旗下账号，绑前必须先解绑旧的）
- ✅ 学校归属覆盖（挂载时主账号 school_id 强覆盖旗下账号）
- ✅ 解绑保留数据（parent_user_id 设回 NULL，旗下账号变孤儿，所有商品/提问保留）

### P2b（鉴权改造 + 数据聚合 + 接收人重定向）

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

bot 权限**严格收窄**到 5 类发布动作（publish_good / question / answer / off_shelf / close_question），所有 app 内交互（评论/点赞/聊天）都走 app，不走 QQ。

### P2c（孤儿账号回复转发 + 请求下架）

由于 P2b 的接收人重定向，**绑定了主账号的旗下号** 已经不需要 P2c：app 用户回复 → 通知主账号 → 主账号在 app 直接处理。P2c 只剩**孤儿旗下号**特殊场景：

- ✅ **创建群持久化**：`users` 加 `created_in_group_id` 字段（详见 `package/sql/migrate_qq_child_orphan_group.sql`）；`BotUpsertQQChild` 创建旗下号时填，`first-seen` 群作为孤儿转发回的目标群
- ✅ **孤儿 inbound 通知转发**：`notification.dispatchInbound` 用 `ResolveInboundTarget` 分流——
  - **InboundNormal** 普通账号 → 入库
  - **InboundBoundChild** 已绑旗下号 → 重定向 parent 后入库（P2b）
  - **InboundOrphan** 孤儿旗下号 → 不入库，调 bot.SendGroup 转发到 `created_in_group_id` 群里 @ 该 QQ；文案区分 4 类（点赞文章/点赞评论/评论/回复评论）+ 官方通知
  - **InboundInvalid** 静默丢弃
- ✅ **孤儿商品 VO**：GET /goods/:id 及列表条目在 owner 是孤儿时返回 `is_orphan_owner: true` + `seller_qq_number: "12345678"`，前端用来切换"联系卖家"按钮：
  - 不是孤儿 → 正常聊天入口
  - 是孤儿 → 弹"通过 QQ 联系：QQ-XXX"告示 + "请求下架"按钮
- ✅ **请求下架**：`POST /api/v1/goods/:id/request-off-shelf` —— bot 在原群里 @ 卖家"是不是已出？回'是'就下架"。失败时清限流锁让用户能重试；同 (caller, good) 1h 内只能请求一次防刷
- 卖家在 QQ 群里回 "是 / 已出 / 鞋架已出" 等 → 走现有 `off_shelf` 识别链路自动下架（不需要新逻辑）

#### P2c 边界 & 回退

- **存量孤儿没 `created_in_group_id`**：迁移前的孤儿 inbound 通知会被静默 drop + log info（不报错）；新创建的旗下号自动填字段不受影响
- **bot 服务不可达**：转发失败仅 log warn，主接口（评论/点赞）仍然成功；用户在 QQ 没收到只是错过一次回复，数据不会错乱
- **请求下架 1h 内同一 caller 同一 good 只允许 1 次**：防 app 用户骚扰卖家
- 孤儿绑回主账号后历史 inbound 自动正常（`ResolveInboundTarget` 下次返 `InboundBoundChild` 走 P2b 路径）

### P3（精度优化 + 边缘）

- 多个在售时反问消歧
- "QQ 加急"功能完整实施：
  - 前端：聊天气泡长按弹菜单 → "加急" → 红色气泡 + "加急"标识
  - 后端：`order_messages` 加 `urgent` / `urged_at` 字段；hfut 加 `POST /orders/:id/messages/:msg_id/urge`；
    bot 内部 HTTP API 加 `POST /internal/qq/send-private`（hfut 调 bot 发私聊），bot 反查接收人 QQ → NapCat send_private_msg
  - 限流：同一对话每 5min 最多加急 1 次；接收人未绑 QQ → 直接拒绝
  - 鉴权：bot 内部 API 用 `BOT_INTERNAL_API_TOKEN` env（hfut 一侧 known，跟 service token 不同）
- 限流 / 错误锁定 / 审计日志
- 重启窗口持久化（如果用了一段时间觉得"丢一半窗口"难受再做；目前接受丢）

---

## 给 Kimi 的运行时提醒（自我约束）

1. **群里发出去的话不可撤回**——上架前再确认一遍信息完整。
2. **路径白名单**：`read_skill` 只能读 `bot/` 和顶层 `SKILL.md`。读 `hfut-union/**` 会被拒绝，**不要尝试**——业务能力走工具集，不要拼 HTTP 调用。
3. **保守**：模糊场景一律不上架；宁可让用户多发一条明确的，不要错上架。
4. **结构化输出**：识别业务动作时必须返回 strict JSON（不要在 JSON 外加自然语言解释）。
5. **窗口内多动作**：一个用户在窗口里可能发了多个商品，工具调用要按顺序逐一处理。
6. **回执友好**：业务真触发后，调 `reply_in_group` @ 用户简短确认（"已为你上架：三层鞋架 6元"），不要长篇大论。
7. **遇到工具 ERROR**：仔细看错误描述、调整参数后重试；不要把 ERROR 直接贴给群友。

---

