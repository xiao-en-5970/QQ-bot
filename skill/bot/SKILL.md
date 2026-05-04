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
| **B. 上架有偿求助** | "5.7 早十代课 30r"、"求人帮带饭" | `goods` (category=2) |
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

---

## 临时账号 / 旗下账号 概念（hfut 后端配套设计，bot 间接依赖）

### 旗下账号是什么

不是"临时账号融合"——而是**主账号永远关联一个 QQ 子账号**：

```
主账号 (account_type=normal, parent_user_id=null)
  └── QQ 旗下账号 (account_type=qq_child, parent_user_id=主账号ID, qq_number=xxx)
```

**关键约束**：
- QQ 旗下账号**永远不能独立登录**（`password` 空 / 标记 inactive）
- 用户名 `qq{qq号}`，昵称 `【QQ】{QQ群名片}`（实时跟群名片同步）
- **严格 1:1**：一个主账号最多 1 个 QQ 旗下账号；要绑第 2 个 QQ 必须先解绑当前 QQ
- bot 运行时所有写操作都是以 QQ 旗下账号身份做的（`user_id` 是子账号 ID）

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

### 数据聚合 / 操作权限

- 主账号的 app 列表（"我的商品" / "我的订单" / 通知）= 主账号自己 + 旗下账号的数据**合并展示**，旗下账号那部分加 tag "来自 QQ"
- 主账号**可以读写**旗下账号全部数据（改、删、回复消息等）
- 主账号 app 内"个人主页"加入口：**进入你的 QQ 智能体** → 进旗下账号空间，**界面受限（只读为主）**
- 不允许主账号操控 bot 主动发 QQ 消息（防 abuse；bot 自己只在群消息触发时回执）
- "QQ 加急"功能：商品聊天界面长按聊天气泡 → 唤起 bot 发 QQ 私聊给对方用户

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

### 限流

- 同一 QQ 5min 内最多请求 1 次验证码（前端按钮置灰 + 后端 redis lock）
- 错 5 次 → 锁 30min（可选，看实际滥用情况再加）

---

## 配置速查（env）

| env | 作用 | 默认 |
|---|---|---|
| `GROUP_AUTO_REPLY_WHITELIST` | 启用自动监听的群号（逗号分隔） | 空 |
| `BOT_AUTO_REPLY_WINDOW_SECONDS` | 同一发送者沉默 N 秒后触发窗口 | 60 |
| `BOT_AUTO_REPLY_MAX_WINDOW_SIZE` | 同一窗口最多攒多少条（防超长） | 20 |
| `COMMANDS_ENABLED` | 启用的子命令（白名单） | 空 = 全禁用 |
| `COMMANDS_DEFAULT` | 无前缀兜底命令 | 空 = 显示菜单 |
| `GPT_API_KEY` | Moonshot API Key | 空 = 不启用聊天 |
| `GPT_MAX_TOOL_ROUNDS` | Kimi 单次 Chat 工具往返上限 | 5 |
| `GPT_MAX_CONTEXT_SIZE` | 单用户保留最近 N 轮对话 | 40 |
| `HFUT_API_URL` | hfut 后端 base URL（P1 启用） | — |
| `HFUT_API_SERVICE_TOKEN` | bot 调 hfut 的 service token（P1 启用） | — |
| `BOT_INTERNAL_API_PORT` | bot 内部 HTTP server 端口（hfut 调 bot 用，P2 启用） | — |
| `BOT_INTERNAL_API_TOKEN` | bot 内部 API 鉴权 token | — |

---

## 实施分期

### P0（**当前阶段**，纯 bot 内部，0 hfut 改动）

- ✅ 加 `BOT_AUTO_REPLY_WINDOW_SECONDS` / `BOT_AUTO_REPLY_MAX_WINDOW_SIZE`
- ✅ `read_skill` 路径白名单（只允许 bot/）
- ⬜ `handleAutoReply` 加 per-(group, user) 滑动窗口聚合，60s 沉默触发
- ⬜ 调 Kimi 识别（系统 prompt 严格判定为业务动作之一才返回结构化 JSON）
- ⬜ **不真上架，只在群里 @ 用户回一句"识别到你想上架/提问/下架 XXX，业务接通中"**
- ⬜ 验证识别准确度

### P1（hfut 后端临时号 + bot 上下架）

- hfut 数据库改造：
  - `users` 加 `account_type` / `qq_number` / `parent_user_id` 字段
  - `articles` 加 `status=close`
  - `goods` `price` 改为 nullable
  - `schools` 加 `qq_groups` 字段（或新建 `school_qq_groups` 表）
- hfut 加 `/api/v1/bot/*` service 接口 + token 鉴权 middleware
- bot 加 hfut 客户端封装 + 工具集实现（publish_good / off_shelf / list_my_active_goods / publish_question / publish_answer / reply_in_group）
- 端到端跑通"识别 → 创建/复用旗下账号 → 上架"

### P2（账号融合 + qq 绑定）

- bot 起内部 HTTP server，暴露给 hfut：check_friend / send_private / send_group
- hfut 加 redis 验证码 + qq 绑定/解绑 API + 旗下账号挂载逻辑
- 前端绑定 / 解绑 / 学校覆盖确认页 + "进入你的 QQ 智能体"入口
- 鉴权改造：主账号能操作自己 + 自己旗下账号的资源；service 层 user_id 校验改造

### P3（精度优化 + 边缘）

- 多个在售时反问消歧
- "QQ 加急"（商品聊天气泡长按 → bot 私聊对方）
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

