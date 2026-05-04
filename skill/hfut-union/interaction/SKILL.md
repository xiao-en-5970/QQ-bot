# interaction — 评论 / 收藏（含收藏夹）/ 点赞

> 父 skill：`../SKILL.md`。这三块都是"通用接口靠 `extType` 区分目标对象"模式：同一个 API 既能评论帖子也能评论商品，只看 `:extType` 路径参数。

## ExtType 对照（重要！经常出错）

| extType | 含义 | 评论可用 | 收藏可用 | 点赞可用 |
|---|---|---|---|---|
| 1 | 帖子 (post) | ✅ | ✅ | ✅ |
| 2 | 提问 (question) | ✅ | ✅ | ✅ |
| 3 | 回答 (answer) | ✅ | ✅ | ✅ |
| 4 | 商品 (good) | ✅ | ✅ | ❌ |
| 5 | 评论 (comment) | ❌ | ❌ | ✅ |

源码权威表是 `validCommentExtTypes` / `validLikeExtTypes` / 收藏的对应数组。**不要拿一个 ext 去乱试不支持的接口**——会被业务错误挡掉。

## 路由表

### 评论

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/api/v1/comments/:extType/:id` | `controller.CommentList` | 列出某对象的根评论 |
| POST | `/api/v1/comments/:extType/:id` | `controller.CommentCreate` | 发评论或回复 |
| GET | `/api/v1/comments/:extType/:id/:commentId/replies` | `controller.CommentListReplies` | 列出某条评论下的回复 |

### 收藏（含收藏夹）

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| POST | `/api/v1/collect/folders` | `CreateCollectFolder` | 新建收藏夹 |
| GET | `/api/v1/collect/folders` | `ListCollectFolders` | 列出我的所有收藏夹 |
| GET | `/api/v1/collect/folders/:id/items` | `ListCollectItems` | 看某收藏夹里的内容 |
| POST | `/api/v1/collect/:extType/:id` | `CollectAdd` | 把某对象加入收藏夹 |
| DELETE | `/api/v1/collect/:extType/:id` | `CollectRemove` | 从收藏夹移除 |

### 点赞

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| POST | `/api/v1/like/:extType/:id` | `LikeAdd` | 点赞 |
| DELETE | `/api/v1/like/:extType/:id` | `LikeRemove` | 取消点赞 |

> 全部需 JWT + LoadUserSchool。

## 详细 — 评论

### GET `/comments/:extType/:id`

列出某对象的**根评论**（一级评论，不包括回复）。

- 路径参数：
  - `:extType` 1–4
  - `:id` 目标对象 ID（帖子 / 提问 / 回答 / 商品 ID）
- Query：`page` / `pageSize`
- 响应：`data: { "list": [response.CommentWithAuthor], "total", "page", "page_size" }`
- Source: `controller/comment.go::CommentList`

### POST `/comments/:extType/:id`

发评论或回复评论（取决于 body 里有没有 `parent_id`/`reply_id`）。

- 入参：`service.CreateCommentReq`：
  ```json
  {
    "content": "...",
    "parent_id": 0,       // 一级评论=0；回复时填被回复评论 ID
    "reply_id": 0,        // 回复某人时填该人的评论 ID（用于"@xxx"展示）
    "images": [...]       // 可选
  }
  ```
- 响应：`data: { "id": <int> }`
- 副作用：
  - 调 `RecordBehavior(Comment)` 行为埋点
  - 调 `Notification().EmitCommentOrReply` 给被评论 / 被回复的人推站内通知
- Source: `controller/comment.go::CommentCreate`
- 注意：handler 里非法 extType 的报错文案写"仅支持 1–3"，但**实际允许 4（商品评论）**，以 `validCommentExtTypes` 为准

### GET `/comments/:extType/:id/:commentId/replies`

列出某条根评论下的回复链。

- 路径参数：`:extType`、`:id`（目标对象）、`:commentId`（被回复的根评论 ID）
- Query：`page` / `pageSize`
- 响应：同 CommentList
- Source: `controller/comment.go::CommentListReplies`

## 详细 — 收藏夹

### POST `/collect/folders`

新建收藏夹。

- 入参：匿名 body `{ "name": "..." }`
- 响应：`data: { "id": <int> }`（收藏夹 ID）
- Source: `controller/collect.go::CreateCollectFolder`

### GET `/collect/folders`

列出当前用户的所有收藏夹（含默认夹）。

- 响应：`data: { "list": [...] }`，每项含 `id` / `name` / `count` 等
- Source: `controller/collect.go::ListCollectFolders`

### GET `/collect/folders/:id/items`

看某收藏夹里的内容。

- 路径参数：`:id` 收藏夹 ID
- Query：
  - `ext_type`：`0` = 全部类型；`1`/`2`/`3`/`4` = 仅该类
  - `page`、`pageSize`
- 响应：`data: { "list": [...], "total", "page", "page_size" }`
- 注：`list` 内部混合了 article enrich 结构 和 good enrich 结构（取决于 ext_type）
- Source: `controller/collect.go::ListCollectItems`

## 详细 — 收藏 / 取消收藏

### POST `/collect/:extType/:id`

把某对象加进收藏夹（默认夹或指定夹）。

- 路径参数：`:extType` 1–4，`:id` 目标对象
- Body：`{ "collect_id": 0 }`（0 表示放进默认夹；非 0 是收藏夹 ID）
- 响应：`ReplyOK`
- 副作用：`RecordBehavior(Collect)`
- 注：`school_id == 0` 时只能收藏公开文章；商品分支不按学校过滤
- Source: `controller/collect.go::CollectAdd`

### DELETE `/collect/:extType/:id`

从收藏夹移除。

- Query：`collect_id`（指定从哪个夹移除；不传 = 默认夹）
- 响应：`ReplyOK`
- 副作用：`RecordBehavior(Uncollect)`
- Source: `controller/collect.go::CollectRemove`

## 详细 — 点赞

### POST `/like/:extType/:id`

- 路径参数：`:extType` 1–5（注意点赞**支持 5=评论**）；`:id` 文章 / 商品 / 评论 ID
- 入参：无 body
- 响应：`ReplyOK`
- 副作用：
  - 文章 / 商品：`RecordBehavior(Like)` + `Notification().EmitLikeArticle`
  - 评论（extType=5）：仅发 `EmitLikeComment` 通知，不打文章行为埋点
- Source: `controller/like.go::LikeAdd`

### DELETE `/like/:extType/:id`

- 入参：无
- 响应：`ReplyOK`
- 副作用：**不发通知**（取消点赞不刷屏）；只反向记录行为
- Source: `controller/like.go::LikeRemove`

## 行为埋点 / 通知关系（速查）

| 操作 | RecordBehavior | Notification |
|---|---|---|
| 评论 / 回复 | `Comment` | `EmitCommentOrReply` |
| 收藏 / 取消 | `Collect` / `Uncollect` | 无 |
| 点赞文章 / 商品 | `Like` | `EmitLikeArticle` |
| 点赞评论 (extType=5) | 无 | `EmitLikeComment` |
| 取消点赞 | `Like(reverse)` | 无 |

埋点用于"为你推荐"召回；通知会在 `notification` 模块的 `GET /notifications` 里被收到。
