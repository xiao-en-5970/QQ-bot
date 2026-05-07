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
- **沉默 60s 触发**（可配置，env `BOT_AUTO_REPLY_WINDOW_SECONDS`，默认 60）
- 同桶内消息合并成一段上下文，整段交给 Kimi
- Kimi 一次返回 N 个动作（可能 0、可能多个）
- 多个发送者交错时**严格按发送者分桶**，不混

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

13. **多图 / 文字主导原则**：判断依据始终是文字诉求；纯图 / 短互动文（`[图1] 看看`、`[图1] 这个怎么样`）一律 type=none，图片不能补救文本不足。
14. **撤回 / 改主意 hard reject**：`算了不卖了`、`刚才那个不算`、`忽略我刚才说的`、`撤回上一条` → 全段 type=none；不替用户做"撤回 + 重新上架"二步操作。
15. **不指代具体物品的 "出" 语句**：`出了 / 都出了 / 出门 / 拿出来` 这种没商品名 + 没价格的句子既不算 publish_good 也不算 off_shelf。

---

## 商品去重（避免重复上架）

同一发布者在 QQ 群里反复发参数高度相似的商品（典型场景：用户先发"出鞋架 6元"，过几个小时又发了一遍来顶帖，或者隔几天补图重发），bot 不能重复创建——会让 hfut 列表被同款商品污染。

### bot 层去重（**publish_good 之前必做**）

1. `publish_good` 调用前**先调** `list_my_active_goods(qq_number)` 拿该用户当前在售清单
2. 把候选项一起喂给 LLM，让模型判断"我现在想发的这条，跟列表里某条是不是几乎一样？"
3. 是 → 不调 `publish_good`，群里 @ 用户回："你这条像之前发的'XXX'，没重复上架（如要重发请先 @bot 下架旧的）"
4. 否 → 走正常 publish_good 流程

### hfut 后端兜底去重

- service 层加 `IsLikelyDuplicate(user_id, category, title, price)` 检查
- 判定标准：同一 user + 同一 category + 标题相似度 ≥ 0.8（ngram / Jaccard）+ 价格差 ≤ 0.1 倍 + 7 天内
- 命中：`POST /goods` 返回 409 + 已存在的商品 ID
- 即使 bot 因模型幻觉漏判，后端也兜底拒绝创建
- **管理员 / 主账号通过 app 上架不受此限制**（人为决定就允许重发）

### 误判保险

- 用户明确说"我重新发一遍 XX 因为图模糊了" → bot 应该先 `off_shelf` 把旧的下了再 `publish_good`，绕开去重
- bot 层判断不准时，hfut 兜底返回 409，bot 收到 409 后**不要慌张**——直接群里 @ 用户："你这条跟之前的'XXX'撞了，要重发请先回'下架旧的'"

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
- 命中限流时返回 `ackKindAskUser` + "发布太频繁，请 Xs 后再来"——**不**调 hfut。

---

## LLM 配额熔断 + regex 兜底识别（P3.6）

### 动机

观测到生产 ak quota 耗尽时（`exceeded_current_quota_error`），bot 仍然**每条新消息都死撞 Moonshot API**，41 小时刷出 60+ 条 ERROR 日志、浪费 RTT、淹没真问题；同时所有窗口 flush 都直接 ERROR 返回，群里业务消息**完全识别不到**。

### quotaGate 熔断器（`utils/kimi/quota_gate.go`）

- 包级单例 `globalQuotaGate`：连续 ≥ `GPT_QUOTA_ERROR_THRESHOLD`（默认 3）次 quota 错 → 进入冷却 `GPT_QUOTA_COOLDOWN_SECONDS`（默认 1800）秒
- LLM 入口（`Chat` / `RecognizeBusinessActions`）调用前先 `IsBlocked()` short-circuit，识别返回 sentinel `kimi.ErrQuotaCooling`
- 任何调用结果通过 `RecordResult(err)` 反馈：成功 / 非 quota 错 都 reset 计数；只 quota 错累计
- 冷却到点自动恢复（lazy 检查时间戳，不起 goroutine）
- 熔断瞬间打一行 WARN log，期间静默——不再每条消息都打 ERROR

### regex 兜底识别（`utils/kimi/recognize_regex.go`）

熔断期间 `auto_reply.go` 调 `kimi.RecognizeViaRegex(input)` 退化识别，**仅识别两类高置信度场景**：

| 模式 | 命中正则 | 命中样例 | 不命中样例 |
|---|---|---|---|
| publish_good 二手 | `^\s*(?:出\|卖)\s*<标题>\s*<价格>\s*[元r块￥]` | "出三层鞋架 6元"、"卖自行车 200块" | "出门"、"出鞋架"（无价）、"出 鞋架 面议" |
| publish_good 求助 | `^\s*(?:代\|求人\|拼\|求带)\s*<标题>\s*<价格>` | "代课 30r"、"拼车去机场 30元" | "求 经验"、"求 资源" |
| off_shelf 短句 | `^\s*(?:已出\|已找到\|出掉了)\s*[!！.]?\s*$` | "已出"、"已找到" | "快乐出门去玩了" |
| off_shelf 带 hint | `<物品> 已出` 或 `已出 <物品>` | "鞋架已出"、"已出鞋架" | — |

**保守原则**（对应 prompt hard rules）：

- hard rule 14：含撤回 / 改主意关键词（`算了`、`不卖了`、`刚才那个不算`、`忽略我刚才`）整条 input 直接 drop
- hard rule 15：纯"出"无标题无价格的句子（`出了`、`都出了`、`出门`）一律 drop
- 价格上限保护：二手 100w 元 / 求助 10w 元，超过的视为误识别
- 纯图片消息 `[图片]` 不参与识别
- 仅识别 `publish_good` + `off_shelf`；问答类（`publish_question` / `publish_answer` / `close_question`）正则误判率太高一律 drop

### ack 文案区分

regex 兜底识别结果的 `Confidence` 固定为 `0.6`（vs LLM 的 0.0~1.0 浮动）。`auto_reply_dispatch.go` 在 ack 文案上**显式区分**：

- LLM 路径：`已为你上架二手「鞋架」：6 元（goods_id=42）`
- regex 兜底：`已（关键词识别）上架二手「鞋架」：6 元（goods_id=42）；如不对请回'撤销'`

让用户感知"现在是兜底模式"，撤销路径靠现有"撤回 hard reject" + "@bot 下架"。
