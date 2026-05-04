# good — 商品（二手 + 有偿求助）

> 父 skill：`../SKILL.md`。商品的 ext_type=4（参与评论 / 收藏，**不参与点赞**）。

## 路由表

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/api/v1/goods` | `GoodList` | 商品列表（支持推荐排序） |
| GET | `/api/v1/goods/:id` | `GoodGet` | 商品详情 |
| POST | `/api/v1/goods` | `GoodCreate` | 新建商品（草稿） |
| PUT | `/api/v1/goods/:id` | `GoodUpdate` | 修改商品 |
| POST | `/api/v1/goods/:id/publish` | `GoodPublish` | 上架 |
| POST | `/api/v1/goods/:id/off-shelf` | `GoodOffShelf` | 下架 |
| POST | `/api/v1/goods/:id/images` | `GoodUploadImages` | 上传商品图（multipart `files`） |
| GET | `/api/v1/user/:id/goods` | `GoodListByUser` | 看某用户在卖什么（在 user 模块路径下） |

> 全部 JWT + LoadUserSchool（除了 `/user/:id/goods` 跟 user 模块一样**没过 LoadUserSchool**——本人列表不按学校过滤）。

## 商品的 `category` 取值

| category | 含义 |
|---|---|
| 1 | 二手 |
| 2 | 有偿求助（任务发布，让别人帮忙做事，收货时付酬） |

## 详细

### GET `/goods`

- Query：
  - `page` / `pageSize`
  - `q`：关键词（标题模糊匹配）
  - `sort`：`updated_at`（默认）/ `recommend`
  - `category`：1 或 2（不传 = 全部）
  - `refresh_token`：仅 `sort=recommend && q==""` 时使用（推荐链路）
- 响应：`data: { "list": [enrichedGoodMap...], "total", "page", "page_size" }`
- 副作用：`stampGoodsViewedBatch(View)` 浏览埋点
- 注意：**有 `q` 关键词时不走推荐**，回退到普通排序
- Source: `controller/good.go::GoodList`

### GET `/goods/:id`

- 路径参数：`:id`
- 响应：单商品 enrich `map`
- 副作用：商品有效且 userID > 0 时调 `RecordBehavior(View)`
- Source: `controller/good.go::GoodGet`

### POST `/goods`

新建商品（草稿状态，需要再 `/publish` 才上架）。

- 入参：`service.CreateGoodReq`（含 `title` / `description` / `price` / `category` / `stock` 等）
- 响应：`data: { "id": <int> }`
- 副作用：未绑学校的用户会业务错
- Source: `controller/good.go::GoodCreate`

### PUT `/goods/:id`

- 入参：`service.UpdateGoodReq`
- 响应：`ReplyOK`
- Source: `controller/good.go::GoodUpdate`

### POST `/goods/:id/publish` / POST `/goods/:id/off-shelf`

- 入参：无
- 响应：`ReplyOK`
- 副作用：商品 `status` 字段状态机变更
- Source: `controller/good.go::GoodPublish` / `GoodOffShelf`

### POST `/goods/:id/images`

multipart 上传，字段名 `files`（复数）。

- 响应：`data: { "urls": [...] }`
- 副作用：把图绑到该商品
- Source: `controller/good.go::GoodUploadImages`

### GET `/user/:id/goods`

挂在 user 模块路径下，但实现在 good 模块。

- 路径参数：`:id` 用户 ID
- Query：`page` / `pageSize`
- 响应：商品 enrich 列表
- 注：本人查自己（`:id` == userID）时显示**包含下架商品**；本人列表 `ownList` 这条路径**不按学校过滤**
- Source: `controller/good.go::GoodListByUser`

## 商品 `status` 状态机

```
草稿(初始) ──Publish──> 上架 ──OffShelf──> 下架 ──Publish──> 上架
                          │
                          └── 与 admin 接口里的 disable / restore 互动
```

具体常量看 `package/constant/types.go`。

## 学校隔离细节

- **公共列表**（`GET /goods`）按学校隔离：未绑学校 → 仅看公开
- **本人列表**（`GET /user/:id/goods` 时 `:id == 自己`）：不按学校过滤，含所有自己发的商品

## 跟交易模块的关系

商品是"被卖的对象"，下单是另一个模块：

```
1. 用户浏览：GET /goods, GET /goods/:id
2. 想买：POST /orders（带 good_id 等）              ← order 模块
3. 跟卖家聊：POST /orders/:id/messages              ← order 模块
4. 卖家确认收款 → 卖家发货 → 买家收货 → 扣库存
```

详见 `./order/SKILL.md`。
