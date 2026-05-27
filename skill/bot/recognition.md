# bot/recognition — 业务识别细节

> 父：`bot/SKILL.md`。本文件展开 bot 在自动监听路径下的识别策略、消歧规则、去重逻辑、图片转存。

主 skill 的 5 类业务动作矩阵 + "准确度优先（宁可漏不可错）"原则是底层硬约束；本文件给具体规则展开。

---

## 窗口聚合（解决 "图 + 文跨多条" 问题）

群里一个上架动作经常跨多条消息：

```
姜糖可乐 22:59:21  [图片]
姜糖可乐 22:59:29  出三层鞋架 6元
姜糖可乐 22:59:43  [图片]
姜糖可乐 22:59:43  出按压U型枕 5元
```

bot 必须把第 1 张图绑给"鞋架"、第 3 张图绑给"U型枕"。规则：

- **per-(group, user) 滑动窗口**——每个 (群, 发送者) 各自一个独立桶
- **沉默 300s 触发**（可配置，env `BOT_AUTO_REPLY_WINDOW_SECONDS`，默认 300）
- 同桶内消息合并成一段上下文，整段交给 Kimi
- Kimi 一次返回 N 个动作（可能 0、可能多个），包含图文配对（image_message_ids）
- 多个发送者交错时**严格按发送者分桶**，不混

**早期"unit 切分立即 flush"机制已废弃**：以前桶里出现"图 图 图 文" 模式会立刻切出一个 unit 提前 flush，目的是让上架回执秒回。实际场景里用户在一次发布中常常图文交杂（图 图 图 视频 商品描述长文 图 价格），早期切分会在第一段文字处截止把后续价格切到下一个 unit 误识别，所以现在所有消息都攒在桶里等 300s 沉默后整体识别。`splitBucketIntoUnits` 函数已移除；只保留 `bucketHasMeaningfulText`——scanOnce 用它选走 Kimi（有文字）还是走 vision OCR（纯图）。

回执延迟代价：上架后约 60s 才回执，但识别准确率显著提升，特别是这类复杂格式：

```
[图1] [图2] [图3] [视频]
"AOC G2490VX显示器，24寸IPS屏，144Hz高刷..."
[图4]
"200 可小刀"
```

会正确识别为一个商品（title="AOC G2490VX显示器"、price=200、bargain=true），而不是切成两条 unit 漏掉价格。

### 视频段处理

QQ 用户在上架时常常附带 [视频]（实物演示），但：

- app 端**不展示视频**——goods.images 只存图片 OSS URL
- bot 也**不下载视频**——QQ 视频文件比图片大一个数量级，下载 + 上传 OSS 的工程成本对识别 / 展示的实际增益接近 0

所以 bot 在 `buildRecognizeInput` 和 `flattenMessageText` 两个出口都直接 **`continue` 跳过 video 段**，不输出 `[视频]` 占位符。Kimi 完全感知不到时间线上有视频，prompt 也不再有"如何处理 [视频]"那条规则。

将来若真要支持视频（比如截首帧当一张商品图），实现路径是独立的：在 dispatch 端加一段"video → ffmpeg 抽首帧 → 上传 OSS → 作为一张 image 加进 images 数组"——跟桶里 video 段是否输出占位符无关。

---

## Reply 段精确下架

如果用户在 QQ 群里**回复**（QQ 客户端"回复消息"功能）自己之前 bot 上架成功的消息说"已出"，bot 不应再问"下架哪件"——reply 段里已经明确指向了某一条具体消息。

数据流：

- **上架时**：`dispatchPublishGood` 调 `collectBotMessageIDs(snap, action)` 收集本次上架涉及到的全部 QQ message_id（snap 里每条外层消息 ID + Kimi 给的 `image_message_ids` / `source_message_ids`），合并去重后通过 `PublishGoodReq.BotMessageIDs` 写到 hfut `goods.bot_message_ids` 列（`BIGINT[]` + GIN 索引）。
- **下架时**：`dispatchOffShelf` 顶部先调 `firstReplyTargetMsgID(snap)` 解析 reply 段的被引用 `message_id`；命中即调 `hfut.LookupActiveGoodByMessageID(userID, msgID)` 反查 `WHERE ? = ANY(bot_message_ids)`；命中返回 good → 直接 `OffShelfGood`，跳过模糊匹配 + 反问消歧。
- **未命中降级**：reply 指向的不是自己的上架消息（reply 别人 / good 已下架），平滑落回原模糊匹配 / 消歧链路。

涉及代码：

- bot：`model.AsReplyData`、`logic/auto_reply_dispatch.go::firstReplyTargetMsgID` + `collectBotMessageIDs` + `dispatchOffShelf` 顶部 reply 通道、`utils/hfut/client.go::LookupActiveGoodByMessageID`
- hfut：`goods.bot_message_ids BIGINT[]` 列（迁移 `migrate_goods_bot_message_ids.sql`）、`dao.Good().FindActiveByBotMessageID`、`service.BotFindActiveGoodByMessageID`、`GET /api/v1/bot/users/:user_id/goods/by-msg/:msg_id`

---

## 纯图 OCR 上架

很多用户用美图秀秀给商品图加水印（"还剩一半 3 元" / "除螨喷雾只用了两次 3 元"）然后直接发图，**不补任何文字**。原行为是 `scanOnce` 沉默后静默清空——会把这部分用户的上架意图全丢。

新分支：

1. `scanOnce` 检测到 silence + `!bucketHasMeaningfulText(b.Msgs)` → 把整桶标 `imageOnly=true`，走 `processImageOnlySnapshot` 而非清空。
2. `processImageOnlySnapshot` 遍历桶里每张图，**单图独立**调 `kimi.RecognizeFromImage(ctx, url, msgID)` 让 Moonshot vision 模型 OCR + 抽取商品信息。
3. 视觉模型固定走 `Cfg.Gpt.VisionModel`（默认 `moonshot-v1-32k-vision-preview`，env `GPT_VISION_MODEL`）；由于 `go-moonshot` SDK 不支持 vision content array，绕过 SDK 走原生 `http.Client` 发请求。
4. 每张图返回的 `RecognizeAction` 强制 `Category=1`、`ImageMessageIDs=[msgID]`、`SourceMessageIDs=[msgID]`，复用 `dispatchActionToHfut` 走"重复 ack / dup ack / 落库 / 写 bot_message_ids" 等完整链路。
5. 单图 OCR 失败 / type=none 跳过下一张；不调多图归一逻辑——这里 prompt 假设是"独立一张图独立一件商品"。

涉及代码：`utils/kimi/vision_ocr.go::RecognizeFromImage`、`logic/auto_reply_image_only.go::processImageOnlySnapshot` + `runVisionOnOneImage`、`logic/auto_reply.go::scanOnce` 的纯图分支。

---

## 合并转发（聊天记录）展开

群里偶尔有人会**一次性转发一整段聊天记录**（QQ 的"合并转发"功能），事件 segment 形如：

```
{"type":"forward","data":{"id":"7234567890123456789"}}
```

直接喂 Kimi 只看得到 `[forward]` 占位符，里面的上架文字 / 配图全丢了。处理路径：

- `logic/handle_at_message.go::handleAutoReply` 检测到 forward segment → 起 goroutine 走
  `logic/forward_expand.go::expandAndPushForward`
- `service/get_forward_msg.go` 调 NapCat `/get_forward_msg` 拿子节点列表
- 每个节点合成"伪 group_message 事件"：**UserID / Sender 用外层转发者**（不用内层 sender，
  避免给不在群里的用户建桶），Segments 用节点的 message
- 按聊天记录顺序依次 `autoReplyMgr.Push` 进**同一桶**，复用窗口聚合 + 三态切分
- 嵌套聊天记录递归展开，最多 3 层；超过深度该层保留原 forward 段
- fetch 失败兜底：原消息仍 Push 一次，让 Kimi 看到 `[合并转发(未展开)]` 占位符

---

## "已出/已找到" 消歧（多在售反问）

实现位置：`logic/disambig_state.go` + `auto_reply.go::Push` + `auto_reply_dispatch.go::dispatchOffShelf/dispatchCloseQuestion`。

- 该用户当前**只有 1 个在售商品** → 直接下架
- 多个在售 → bot @ 用户反问（消歧上下文 TTL 60s）：

  > 要下架哪一条？回 1/2/3 即可：  
  > 1. 三层鞋架  
  > 2. 按压U型枕  
  > 3. 自行车

  存 `disambigContext{Kind, UserID, GroupID, Candidates, CreatedAt}`。
- 用户回 `1` / `2` / `①` 等单字数字 → `auto_reply.go::Push` 立刻 flush（不等 silence），秒回。
- 整段窗口都是单条数字选择 → `dispatchDisambigChoice` 直接调 `OffShelfGood/CloseArticle`。
- 用户改话题（窗口里有非数字消息或多条） → `disambigMgr.Clear(key)` 后走正常 Kimi 识别。
- TTL 过期不主动清理，靠 `Get/Take` 时懒过期。
- "1 还有这个鞋架也卖 5 块" 这种含数字 + 新意图的窗口**不**算消歧（`windowAsDisambigChoice` 要求 `len(snap)==1`）。
- 候选展示上限 5 条，超过显示前 5 + 省略号。

---

## 提问 vs 商品的判别

- **提问句式典型**："有人有 XX 吗"、"问下大家 XX"、"求 XX"（求资源、求经验，不带价格）
- **商品句式典型**："出 XX N元"、"卖 XX"、"求人 XX 30r"（带价格 / 带"代"字）
- **模糊时优先判不上架**（保守）

---

## 提问的 status=close

- hfut `articles` 表加 `status=close` 表示提问被关闭、不接受回答
- 用户**主动**在前端关闭，bot 不会自己关闭
- bot 提交回答前要先查这个 question 是否 status=close，是则 drop

---

## Kimi prompt hard rules（P3.5 增补的关键约束）

定义在 `utils/kimi/recognize.go::recognizeSystemPrompt`：

13. **多图 / 文字主导原则**：判断依据始终是文字诉求；纯图 / 短互动文（`[图1] 看看`、`[图1] 这个怎么样`）一律 type=none，图片不能补救文本不足。**上架信号至少要满足"动词信号 OR 价格信号"之一**：有 `出/卖/转让/求/收/求购` 等动词 → 走 publish_good / seek_goods；或者有明确价格（`5r` / `30 元` / `免费` / `面议`）→ 走隐式上架 publish_good cat=1（典型卖家晒图）。两者都没的多图 + "讨内容"语气（`来点 X` / `想要点 X` / `给我来几张 X` / `求张高清原图`）→ 全部 type=none，**不要**脑补成 publish_good。
14. **撤回 / 改主意 hard reject**：`算了不卖了`、`刚才那个不算`、`忽略我刚才说的`、`撤回上一条` → 全段 type=none；不替用户做"撤回 + 重新上架"二步操作。
15. **不指代具体物品的 "出" 语句**：`出了 / 都出了 / 出门 / 拿出来` 这种没商品名 + 没价格的句子既不算 publish_good 也不算 off_shelf。

---

## 商品去重（避免重复上架）

同一发布者在 QQ 群里反复发参数高度相似的商品（典型场景：用户先发"出鞋架 6元"，过几个小时又发了一遍来顶帖，或者隔几天补图重发），bot 不能重复创建——会让 hfut 列表被同款商品污染。

### bot 层去重（**publish_good 之前必做**）

1. `publish_good` 调用前**先调** `list_my_active_goods(qq_number)` 拿该用户当前在售清单
2. 把候选项一起喂给 LLM，让模型判断"我现在想发的这条，跟列表里某条是不是几乎一样？"
3. 是 → 不调 `publish_good`，群里 @：`「XXX」已在售，要重发先回下架旧的`
4. 否 → 走正常 publish_good 流程

### hfut 后端兜底去重

- service 层加 `IsLikelyDuplicate(user_id, category, title, price)` 检查
- 判定标准：同一 user + 同一 category + 标题相似度 ≥ 0.8（ngram / Jaccard）+ 价格差 ≤ 0.1 倍 + 7 天内
- 命中：`POST /goods` 返回 409 + 已存在的商品 ID
- 即使 bot 因模型幻觉漏判，后端也兜底拒绝创建
- **管理员 / 主账号通过 app 上架不受此限制**（人为决定就允许重发）

### 误判保险

- 用户明确说"我重新发一遍 XX 因为图模糊了" → bot 应该先 `off_shelf` 把旧的下了再 `publish_good`，绕开去重
- hfut 返 409 时群里 @：`「XXX」已在售，要重发先回：下架旧的`

---

## 图片转存（NapCat 临时 URL → hfut OSS 永久 URL）

NapCat 在 `image` segment 里给的 URL 是腾讯多媒体的临时签名链接，**几天后会失效**。如果 bot 直接把这个 URL 入 hfut goods.images，过几天用户在 app 看商品就只剩死链。

### 链路（`logic/auto_reply_dispatch.go`）

1. dispatch 拿到 RecognizeAction 后，先调 `imageURLsFromSnap` 找出 NapCat 临时 URL
2. 调 `mirrorImagesToHfut(ctx, userID, urls)`：每张图独立走"GET NapCat URL → multipart POST 到 hfut → 拿永久 URL"
3. 把转存后的永久 URL 列表传给 `PublishGood` / `PublishArticle` 入库

### hfut 端接口

`POST /api/v1/bot/images` (multipart/form-data)

- 字段：`file`（二进制）+ `user_id`（int）
- 走 `BotServiceAuth` 中间件，跟其他 bot 接口一致
- 存到 OSS 路径 `user/{user_id}/bot/img_{snowflake}.{ext}`——用 user/ 前缀的"用户级图床"避开"good_id 还没建"的鸡生蛋问题
- 限制：单张 ≤ 10MB；扩展名白名单 jpg/jpeg/png/gif/webp
- 返回 `{ url: "<完整可达的 OSS URL>" }`

### 失败处理（关键设计）

- **每张图独立**：循环里 try-catch 每张图的下载+上传，**任一张失败仅 skip 那张** + log warning，不阻塞其它图
- **下载超时 15s / 上传超时 30s**：保证 dispatch 整体不会因为图片网络抖动卡死
- **大小预检**：`io.LimitReader` 截断，超过 10MB 直接拒收，防 OOM
- **0 张成功也允许商品发布**：`goods.images=[]` 比"商品创建失败"友好——商品至少是真实的

### 清理策略（暂未实现）

- `user/{id}/bot/img_*` 路径下的图随 good 创建/删除不级联清理（路径里没有 good_id 关联）
- 长期会堆积；P3 阶段加 cron 任务"清理 30 天前的 user/*/bot/img_* 文件"

---

## bot dispatch 限流（防误识别 / 滥用 / 自动化脚本刷屏）

实现：`logic/disambig_state.go::dispatchRateLimiter`，per-(group, user) 滑动窗口；窗口 1min，阈值 3 次。

- 仅对**会改 hfut 状态**的 action 计数：`publish_good` / `publish_question` / `publish_answer`。
- `off_shelf` / `close_question` 多候选时只是反问 + 等回应，**不计数**，避免用户消歧时反被限流。
- 命中限流时：`太快，Xs 后再发`，**不**调 hfut。

---

## LLM 配额熔断 + 静默丢弃（P3.6）

### 动机

观测到生产 ak quota 耗尽时（`exceeded_current_quota_error`），bot 仍然**每条新消息都死撞 Moonshot API**，41 小时刷出 60+ 条 ERROR 日志、浪费 RTT、淹没真问题。

### quotaGate 熔断器（`utils/kimi/quota_gate.go`）

- 包级单例 `globalQuotaGate`：连续 ≥ `GPT_QUOTA_ERROR_THRESHOLD`（默认 3）次 quota 错 → 进入冷却 `GPT_QUOTA_COOLDOWN_SECONDS`（默认 1800）秒
- LLM 入口（`Chat` / `RecognizeBusinessActions`）调用前先 `IsBlocked()` short-circuit，识别返回 sentinel `kimi.ErrQuotaCooling`
- 任何调用结果通过 `RecordResult(err)` 反馈：成功 / 非 quota 错 都 reset 计数；只 quota 错累计
- 冷却到点自动恢复（lazy 检查时间戳，不起 goroutine）
- 熔断瞬间打一行 WARN log，期间静默——不再每条消息都打 ERROR

### 冷却期 / 单次 quota 错：直接静默

**`auto_reply.go` 收到 `ErrQuotaCooling` 或 `IsQuotaError` 一律 `return`**——不做任何兜底识别。

为什么不做正则兜底：

- **正则做语义识别不可靠**——动词前缀 / 数字识别 / 多义词 / 否定语气全是坑，false-positive 会把
  闲聊错落库成商品 / 求物品；用户体感比"暂时没识别"差得多。
- 配额耗尽通常 30 分钟～小时级恢复，宁可少识别一点也不要错落库；运营侧靠
  ops 群的 `[ops] 上架` 转发能立即发现"群里没动静"，及时充值。
- 撤销 / 关闭 等高频"短句"业务（`不要了 / 已出 / 已找到 / 求到了`）由 Kimi 走窗口
  化识别已经足够稳；让 bot 在冷却期完全闭嘴，不引入半吊子识别。

文案原则：**不暴露专业 ID/技术字段**（goods_id、article_id 等）——用户在 app 里能看到自己刚发的内容，回执只确认"做了什么 + 关键属性"。详见 `skill/bot/verbosity.md`。
