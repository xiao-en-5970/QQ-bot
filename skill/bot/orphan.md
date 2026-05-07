# bot/orphan — 孤儿旗下账号特殊行为

> 父：`bot/SKILL.md`。  
> 兄弟：`account-model.md`（账号模型基础）。

"孤儿"= 旗下账号的 `parent_user_id` 为 null（之前被解绑、或还没人绑过）。**孤儿账号在 app 里仍然可见**（它发布的商品 / 问答还存在），但功能严重受限——因为没有"在线人"对应。

---

## inbound 分流（`notification.dispatchInbound`）

`ResolveInboundTarget` 把所有 inbound 通知分 4 类：

| 分类 | 处理 |
|---|---|
| `InboundNormal` 普通账号 | 入库 |
| `InboundBoundChild` 已绑旗下号 | 重定向 parent 后入库（见 `account-model.md`） |
| `InboundOrphan` 孤儿旗下号 | 不入库，调 `bot.SendGroup` 转发到 `created_in_group_id` 群里 @ 该 QQ |
| `InboundInvalid` | 静默丢弃 |

---

## 孤儿的问答 / 评论收到 app 内回复 → bot 转发回当年的群

```
孤儿账号 child-A（在 QQ 群 G 里创建过、问过"有人有形势与政策题库吗"）
  ↓ 别人在 app 里给这条问答回复："我有，私聊我"（app 里的回答者是真实主账号 U）
  ↓
[hfut] dispatchInbound 检测到回复目标是孤儿 → 调 bot：转发到群 G
  ↓
[bot] 在群 G 里发：「【来自 app 用户 U】对你那条提问'XX'的回答：我有，私聊我」
```

注意：

- 转发文案**标注"来自哪个 app 用户"**，不冒充 app 用户身份；让群里那个原始 QQ 用户能看到、自行决定要不要去加 app 联系
- 文案区分 4 类（点赞文章/点赞评论/评论/回复评论）+ 官方通知
- **存量孤儿没 `created_in_group_id`** 的 inbound 通知会被静默 drop + log info（不报错）；新创建的旗下号自动填字段不受影响
- bot 服务不可达：转发失败仅 log warn，主接口（评论/点赞）仍然成功；用户在 QQ 没收到只是错过一次回复，数据不会错乱

---

## 孤儿的商品被 app 用户想买 → 不开放聊天，只挂告示

孤儿账号挂的商品在前端：

- **不**显示"我想要 / 我来接"按钮（不允许下单——孤儿不在 app 里没法做订单聊天）
- 改为展示一段告示 + "请求下架"按钮（**所有 category 统一文案，不再加"已出"等后缀**）：

```
该卖家通过 QQ 联系：QQ-12345678
请直接 QQ 沟通；如已不再有效，请点击"请求下架"
```

实现契约：

- 后端 VO（`enrichGoodWithAuthor`）在 owner 是孤儿时返回 `is_orphan_owner: true` + `seller_qq_number: "12345678"`
- 前端 `GoodDetailScreen` 检测到 `is_orphan_owner=true` → 切换分支渲染（详见 `hfut-front/src/screens/GoodDetailScreen.tsx`）；按钮无论 category 都只展示"请求下架"

---

## 请求下架（手动审计的兜底）

`POST /api/v1/goods/:id/request-off-shelf` —— bot @ 发布者，按 category 选择问句（不出现 goods_id，**必须明显区分**让发布者知道在问什么）：

- `category=1`（二手卖东西）：`「标题」已经出了吗？请回答是或不是。`
- `category=2`（求物品）：`「标题」是否已经求得该物品？请回答是或不是。`

**手动审计**避免 app 用户恶意点别人的"下架"。

- 同 `(caller, good)` 1h 内只能请求 1 次（防 app 用户骚扰发布者）
- 通知通路三级 fallback（让旧数据 / 跨群发布 / bot-非好友 各种边界都能尽量送达）：
  1. 商品自己的 `goods.created_in_group_id`（bot 上架时记录，最精准）→ `SendGroup` @ 发布者
  2. fallback 到 `users.created_in_group_id`（owner 首见群）→ `SendGroup` @ 发布者
  3. 都缺失 / 全失败 → `CheckFriend`，是 bot 好友就 `SendPrivate`；否则前端弹"请直接通过 QQ 联系"
- 三级全失败时清限流锁让用户能重试
- 发布者在群里的肯定回复 → 走现有 `off_shelf`（无需新接口）：
  - cat=1：`是` / `已出` / `已经出了` / `出掉了` / `XX 已出`
  - cat=2：`是` / `已找到` / `找到了` / `已求得` / `求到了` / `已求到` / `已买到` / `买到了` / `XX 已找到`
  - 否定回复（`不是` / `没出` / `还没求到` / `还需要`）→ 不动作，发布者继续保留商品 / 求购

字段持久化路径：
- `goods.created_in_group_id` 由 `BotPublishGood` 在落库时写入（`PublishGoodReq.GroupID = bot 收到该消息的 QQ 群号`）；非 bot 路径上架（管理员 / app 直传）的商品保持 NULL
- 存量孤儿（迁移前已存在）`users.created_in_group_id` 也可能为 NULL，会直接走第 3 级 fallback

---

## 孤儿账号绑回主账号后

旗下号被某个主账号绑定（无论是原 owner 还是新主账号绑的同一 QQ）→ 孤儿状态消失：

- 商品的"联系卖家"恢复正常聊天窗口（聊天对端是新挂上来的主账号）
- 问答的回复正常进 app 通知，不再转发回群
- 主账号能在自己的聊天页 / "进入你的 QQ 智能体" 受限界面里看到所有相关聊天
- 之前 app 用户在孤儿期发的回复、留言要做**保留并可见**（不要丢，这是用户的合理预期）
- 历史 inbound 自动正常（`ResolveInboundTarget` 下次返 `InboundBoundChild` 走绑定路径）
