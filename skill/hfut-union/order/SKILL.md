# order — 订单 + 买卖双方聊天

> 父 skill：`../SKILL.md`。**平台不经手资金**——这是手工状态机：下单 → 卖家确认收款 → 派送 / 自提 → 买家确认收货 → 扣库存。

## 订单状态机（走通一笔的步骤）

```
1. 买家：POST /orders                 创建订单（占库存逻辑，service 内部）
2. 买家把钱通过线下方式给卖家（微信 / 支付宝 / 见面给现金 / 等等）
3. 卖家：POST /orders/:id/seller-confirm-payment    卖家确认收到钱了
4. 卖家：POST /orders/:id/confirm-delivery          卖家点击"已发货 / 已交付"
5. 买家：POST /orders/:id/confirm-receipt           买家点击"已收货"
   ↑ 这一步会**真正扣商品库存**；扣失败（库存不足）时整笔 fail
```

中间任何一方都可以 `POST /orders/:id/cancel` 取消（受当前状态约束）。

"有偿求助"（good.category=2）的订单还有一个特别接口：`POST /orders/:id/help/pay` —— 求助者上传付酬截图给做事的人看。

## 路由表

### 买家 / 卖家通用

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| POST | `/api/v1/orders` | `OrderCreate` | 下单 |
| GET | `/api/v1/orders` | `OrderList` | 我作为**买家**的订单列表 |
| GET | `/api/v1/orders/sold` | `OrderListSold` | 我作为**卖家**的订单列表 |
| GET | `/api/v1/orders/:id` | `OrderGet` | 订单详情（buyer 或 seller 才能看） |
| PUT | `/api/v1/orders/:id` | `OrderUpdate` | 卖家更新发货地址 |
| POST | `/api/v1/orders/:id/cancel` | `OrderCancel` | 取消订单 |

### 状态机推进

| 方法 | 路径 | Handler | 谁调用 |
|---|---|---|---|
| POST | `/api/v1/orders/:id/seller-confirm-payment` | `OrderSellerConfirmPayment` | 卖家 |
| POST | `/api/v1/orders/:id/confirm-delivery` | `OrderConfirmDelivery` | 卖家 |
| POST | `/api/v1/orders/:id/confirm-receipt` | `OrderConfirmReceipt` | 买家（**会扣库存**） |
| POST | `/api/v1/orders/:id/help/pay` | `OrderHelpPublisherPay` | 有偿求助发布者上传付酬截图 |
| POST | `/api/v1/orders/:id/location` | `OrderLocationUpdate` | 买方改收货地 / 卖方改发货地 / 卖方确认或拒绝买方改址 |

### 订单聊天

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/api/v1/orders/:id/messages` | `OrderMessagesList` | 看消息记录 |
| POST | `/api/v1/orders/:id/messages` | `OrderMessageCreate` | 发消息 |
| POST | `/api/v1/orders/:id/messages/read` | `OrderMessagesMarkRead` | 标记已读（默认全标 + 可选 `last_read_message_id`） |

> 全部 JWT + LoadUserSchool。

## 详细

### POST `/orders`

下单。

- 入参：`service.CreateOrderReq`：
  ```json
  {
    "good_id": 1,
    "quantity": 1,
    "buyer_location_id": 5,      // 买方收货地址 ID
    "remark": "..."
  }
  ```
- 响应：`data: { "id": <int> }`
- 副作用：service 层校验商品 / 库存 / 学校归属 / 收货地址；**占库存**逻辑在这里
- 常见错误：`库存不足` / `商品未上架` / `地址不存在` / `不能下单自己的商品`
- Source: `controller/order.go::OrderCreate`

### GET `/orders` 和 GET `/orders/sold`

- Query：`page` / `pageSize` / `status`（按订单状态过滤）
- 响应：`data: { "list": [orderToMap...], "total", ... }`
- `orderToMap` 内嵌商品摘要（`good`）、买家 / 卖家信息
- 区别：`/orders` 是**我买的**；`/orders/sold` 是**我卖出去的**
- Source: `controller/order.go::OrderList` / `OrderListSold`

### GET `/orders/:id`

- 路径参数：`:id`
- 响应：`data: orderToMap`（详情含状态、金额、双方信息、收发地址等）
- 校验：必须是 buyer 或 seller，否则 403
- Source: `controller/order.go::OrderGet`

### PUT `/orders/:id`

卖家更新自己的**发货地址**（不改其它字段）。

- 入参：`service.UpdateSellerAddrReq`
- 响应：`ReplyOK`
- Source: `controller/order.go::OrderUpdate`

### POST `/orders/:id/cancel`

- 入参：`service.CancelOrderReq`（含 `reason` 等）
- 响应：`ReplyOK`
- 状态机限制：只有处于"未收货"系列状态才能取消；具体看 service 层 `Cancel` 错误文案
- Source: `controller/order.go::OrderCancel`

### 状态机推进 4 个 API

各自 body 多为 `service.<ActionName>Req`（部分 handler 把 Bind 写成 `_ =`，body 可选/无关紧要）。

| API | 谁调 | 关键副作用 |
|---|---|---|
| `seller-confirm-payment` | 卖家 | 状态从"已下单"推到"已确认收款" |
| `confirm-delivery` | 卖家 | 状态推到"已发货"；含 `delivery_method` 等字段 |
| `confirm-receipt` | 买家 | **扣商品库存**；可能因 `ErrOrderInsufficientStock` 失败 |
| `help/pay` | 有偿求助发布者 | 上传付酬截图（multipart）让做事人看到 |

错误处理：每一步都对当前订单状态做校验。如果状态不对（比如还没发货就点确认收货），会返回业务错。具体错误文案在 service 层。

Source: `controller/order.go`

### POST `/orders/:id/location`

统一更新订单地址：

- **买方**调：改自己的收货地（如果状态允许）
- **卖方**调：改发货地，或确认 / 拒绝买方刚刚提交的地址改动

- 入参：service 层有专门的 req 结构
- 响应：`ReplyOK`
- Source: `controller/order.go::OrderLocationUpdate`

## 订单聊天

### POST `/orders/:id/messages`

- 入参：`service.CreateOrderMessageReq`：
  ```json
  {
    "content": "...",
    "type": "text",       // text / image / etc.
    "image_url": "..."    // 当 type=image 时
  }
  ```
- 响应：`data: { "id": <int> }`（消息 ID）
- 副作用：推送给对方未读 +1（在 `/user/chat/unread` 里能看到）
- Source: `controller/order_chat.go::OrderMessageCreate`

### GET `/orders/:id/messages`

- Query：`page` / `pageSize` / `before_id`（向上翻历史，用最旧消息的 id 做游标）
- 响应：消息列表分页
- Source: `controller/order_chat.go::OrderMessagesList`

### POST `/orders/:id/messages/read`

- 入参（可选）：`{ "last_read_message_id": <int> }`；不传 = 标记全部已读
- 响应：`ReplyOK`
- Source: `controller/order_chat.go::OrderMessagesMarkRead`

### 配套：未读数

`GET /api/v1/user/chat/unread`（在 user 模块）返回所有订单未读汇总。

## 关于"平台不经手资金"

设计上：
- 没有任何"支付"接口
- 没有第三方支付集成
- 钱怎么从买家给卖家是用户自己解决（线下）
- bot 只负责"双方点确认按钮"的状态机 + 聊天 + 库存

风控约束：
- 卖家在确认收款前可以撤单；**确认收款后**取消必须协商，会推问题给客服 / admin
- 买家点确认收货 = 库存扣减 = 订单 final（再有问题走 admin 工单）

## VO 引用

- `service.CreateOrderReq` / `UpdateSellerAddrReq` / `CancelOrderReq` / `CreateOrderMessageReq` 等都在 `app/service/order.go` / `order_chat.go`
- `orderToMap` 在 `controller/order.go` 内部，含商品摘要 + 双方资料
