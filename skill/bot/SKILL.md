# bot — bot 自身的能力 / 行为约束 / 自动回复策略

> 父 skill：`../SKILL.md`（顶层索引）。
>
> 这个 skill 不描述 HTTP API，而是描述 **bot 自己怎么响应群消息、什么时候该说话、什么时候该闭嘴**。Kimi 在群里被触发时应当先 `read_skill(path="bot/SKILL.md")` 拿到本文件，再决定怎么回应。
>
> ⚠️ **重要约束**：bot 运行时**只能读 `bot/` 子树和最顶层 `SKILL.md`**，**禁止读 `hfut-union/` 子 skill**。`hfut-union/` 那一套 API 文档是给开发者写代码时参考的，权限太高、不应该让 Kimi 在跟用户对话时直接拼 HTTP 调用——业务能力必须经过下面"工具集"那一层封装才能用。`read_skill` 工具会强制路径白名单。

---

## 渐进式披露：分文件结构

主文件（本文）只列必备的核心规则 + 工具速查 + 子文件索引。**详细规则按需进一步读**：

| 你想了解 | 读这个 |
|---|---|
| 5 类业务动作的窗口聚合 / 消歧 / 提问 vs 商品判别 / 商品去重 / 图片转存 / dispatch 限流 / Kimi prompt hard rules | `read_skill(path="bot/recognition.md")` |
| 主账号 ↔ QQ 旗下账号关系 / 接收人重定向 / "账号集"权限模型 / 学校归属 / 数据聚合 / 自交易防护 | `read_skill(path="bot/account-model.md")` |
| 孤儿旗下号特殊行为：inbound 转发回群、商品请求下架、绑回主账号 | `read_skill(path="bot/orphan.md")` |
| QQ 绑定 / 解绑流程 / 加急通路 / 错码锁 / 错误回执表 / 服务调用审计 | `read_skill(path="bot/qq-bind.md")` |
| 自动回复 verbose / normal 模式（运维 / 调试） | `read_skill(path="bot/verbosity.md")` |
| P0 ~ P3 实施分期归档（开发者回顾） | `read_skill(path="bot/phases.md")` |

> **省钱省 token 的读法**：先读完本主文件知道大概后，**只在用户问题真的涉及具体子模块时才读子文件**。比如普通"出鞋架 6 元"这种典型上架场景，读完主文件 + `recognition.md` 即可，**不需要**读 account-model / orphan / qq-bind。

---

## bot 是什么

接到 NapCat WebSocket 推送的群消息事件，识别两类触发：

1. **@bot 命令路径**：`@bot jm 350234` / `@bot chat 你好` 等显式指令
2. **白名单群自动监听路径**：用户在群里说"出鞋架 6 元"、"代课 30r"、"已出"等业务句子，bot 自动识别 → 在 hfut-union app 里同步上下架 / 提问 / 回答

### 触发模式 1：@bot 命令路径

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

### 触发模式 2：群聊白名单自动监听路径

群号在 `conf.Cfg.Group.AutoReplyWhitelist` 里 → 抓取所有非 bot 自己发的群消息（不需要 @bot），由 LLM 判断是不是 5 类业务动作之一并自动同步到 hfut-union app。

入口代码：`logic/handle_at_message.go::handleAutoReply`。

---

## 5 类业务动作（核心识别契约）

| 动作 | 群里典型句式 | 同步到 hfut |
|---|---|---|
| **A. 上架二手** | "出三层鞋架 6元 [图片]"、"出按压U型枕 5元" | `goods` (category=1)，价格不填 → 面议 |
| **B. 上架有偿求助 / AA / 拼团** | "5.7 早十代课 30r"、"求人帮带饭"、"出去玩 人均 20r"、"拼车去机场 AA 30" | `goods` (category=2)；前端文案"求助/活动" |
| **C. 上架提问** | "有人有形势与政策8的题库吗"（向整个群提问） | `articles` (type=2 question) |
| **D. 提交回答** | 别人提问后某用户精准答了 | `articles` (type=3 answer)，挂到原 question 下 |
| **E. 下架/已找** | "鞋架已出"、"已找到" / "已出"（不指明） | `POST /goods/:id/off-shelf` 或 `articles.status=close` |

### 准确度优先（**宁可漏，不可错** —— 这条最重要）

- 模糊判定一律 drop（不打扰用户）
- 真触发上架时**必须 @ 用户确认**（"@用户 已为你上架：三层鞋架 6元"）
- **不需要管理员预览**——目标是"用户群里发一句无感同步到 app"
- 模糊时优先判**不上架**（保守）
- 跨多条消息要按"窗口聚合"处理（详见 `recognition.md`）

---

## 工具集（Kimi 在自动监听路径下的全部能力）

> Kimi 不能直接拼 hfut HTTP 调用。所有跟 hfut 的交互必须走下面这些封装好的工具。

### 现已实现

| 工具 | 用途 |
|---|---|
| `read_skill(path)` | 读 bot 自身 skill 文档（路径白名单：`bot/**` + 顶层 `SKILL.md`） |
| `publish_good(qq_number, title, description, price?, category, location?, image_urls)` | 上架商品；price 不传 = 面议 |
| `off_shelf_good(qq_number, good_id?)` | 下架；不传 good_id 时自动选该用户唯一在售（多个则先 `list_my_active_goods`） |
| `list_my_active_goods(qq_number)` | 查该用户当前在售商品（消歧 + 去重用） |
| `publish_question(qq_number, title, content, image_urls)` | 创建提问 |
| `publish_answer(qq_number, parent_question_id, content)` | 提交回答 |
| `list_recent_open_questions(group_id)` | 查群内最近未关闭的提问（定位 parent_question_id） |
| `reply_in_group(group_id, at_qq, text)` | bot 在群里 @ 用户回执（"已为你上架 XXX"） |

### 工具调用规则

- **禁止**：让 Kimi 直接生成 / 推荐 hfut HTTP URL
- **禁止**：让 Kimi 跨用户操作（操作 A 用户的商品时不能传 B 的 qq_number；下层鉴权也会兜底）
- **建议**：先调"查询类"工具确认状态（`list_my_active_goods` / `list_recent_open_questions`）再调"写入类"工具
- 商品去重：`publish_good` 前**必须**先调 `list_my_active_goods`（详细规则见 `recognition.md`）

---

## 配置速查（关键 env）

| env | 作用 | 默认 |
|---|---|---|
| `GROUP_AUTO_REPLY_WHITELIST` | 启用自动监听的群号（逗号分隔） | 空 |
| `BOT_AUTO_REPLY_WINDOW_SECONDS` | 同一发送者沉默 N 秒后触发窗口 | 60 |
| `BOT_AUTO_REPLY_MAX_WINDOW_SIZE` | 同一窗口最多攒多少条（防超长） | 20 |
| `GROUP_AUTO_REPLY_VERBOSITY` | 自动回复模式：`verbose` / `normal`（详见 `verbosity.md`） | verbose |
| `COMMANDS_ENABLED` | 启用的子命令（白名单） | 空 = 全禁用 |
| `GPT_API_KEY` | Moonshot API Key | 空 = 不启用聊天 |
| `HFUT_API_URL` | hfut 后端 base URL | — |
| `HFUT_API_JWT_SECRET` | bot 跟 hfut 共享的 service-to-service JWT secret（HS256） | — |
| `BOT_INTERNAL_API_PORT` | bot 内部 HTTP server 端口（仅 internal network 监听） | 8090 |

---

## 给 Kimi 的运行时提醒（自我约束）

1. **群里发出去的话不可撤回**——上架前再确认一遍信息完整。
2. **路径白名单**：`read_skill` 只能读 `bot/**` 和顶层 `SKILL.md`。读 `hfut-union/**` 会被拒绝，**不要尝试**——业务能力走工具集，不要拼 HTTP 调用。
3. **保守**：模糊场景一律不上架；宁可让用户多发一条明确的，不要错上架。
4. **结构化输出**：识别业务动作时必须返回 strict JSON（不要在 JSON 外加自然语言解释）。
5. **窗口内多动作**：一个用户在窗口里可能发了多个商品，工具调用要按顺序逐一处理。
6. **回执友好**：业务真触发后，调 `reply_in_group` @ 用户简短确认（"已为你上架：三层鞋架 6元"），不要长篇大论。
7. **遇到工具 ERROR**：仔细看错误描述、调整参数后重试；不要把 ERROR 直接贴给群友。
8. **渐进式披露**：本主文件够用就不要再 read_skill。**只**在用户问到子模块（绑定 QQ / 孤儿账号 / 加急 / dispatch 限流原理 / 学校归属等）才进一步 read 对应子文件。
