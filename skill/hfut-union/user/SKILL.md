# user — 用户身份 / 资料 / 个人主页 / 收货地址

> 父 skill：`../SKILL.md`。注意本模块**所有路由都在 `LoadUserSchool` 中间件之前**，handler 里 `school_id` 上下文恒为 0；数据可见性靠 service 内部读 DB 拿用户当前 school_id 实现。

## 路由表

### 当前用户

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/api/v1/user/info` | `UserInfo` | 当前用户完整信息 |
| POST | `/api/v1/user/update` | `UserUpdate` | 改资料（昵称/简介/性别等） |
| POST | `/api/v1/user/bind/school` | `UserBindSchool` | 绑定学校（前置：`/user/school-login`） |
| POST | `/api/v1/user/avatar` | `UserUploadAvatar` | 上传头像（multipart） |
| POST | `/api/v1/user/background` | `UserUploadBackground` | 上传主页背景 |
| GET | `/api/v1/user/chat/unread` | `UserChatUnreadSummary` | 订单聊天未读汇总 |
| GET | `/api/v1/user/collects` | `UserListCollects` | 当前用户的收藏列表（按 ext_type） |

### 收货地址

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/api/v1/user/locations` | `UserLocationList` | 列出我的所有地址 |
| POST | `/api/v1/user/locations` | `UserLocationCreate` | 新增地址 |
| PUT | `/api/v1/user/locations/:id` | `UserLocationUpdate` | 改地址 |
| DELETE | `/api/v1/user/locations/:id` | `UserLocationDelete` | 删地址 |
| POST | `/api/v1/user/locations/:id/default` | `UserLocationSetDefault` | 设为默认 |

### 看别人主页（公开身份信息）

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/api/v1/user/:id` | `UserProfile` | 任意非删用户的公开身份 |
| GET | `/api/v1/user/:id/posts` | `UserListPosts` | 该用户的帖子（自己看自己含私密；看别人仅公开） |
| GET | `/api/v1/user/:id/questions` | `UserListQuestions` | 该用户的提问 |
| GET | `/api/v1/user/:id/answers` | `UserListAnswers` | 该用户的回答 |
| GET | `/api/v1/user/:id/goods` | `GoodListByUser` | 该用户在卖的商品（本人列表 `ownList` 不按学校过滤） |

## 详细 API

### GET `/user/info`

- 入参：无
- 响应：`data: response.UserInfo`（含 `id` / `username` / `nickname` / `avatar` / `background` / `school_id` / `role` / `status` / `created_at` 等。`school_id` 来自 DB，handler 上下文里也没有）
- Source: `controller/user.go::UserInfo`

### POST `/user/update`

- 入参：`service.UpdateProfileReq`（昵称 / 简介 / 性别 / 出生日期 等）
- 响应：`ReplyOK`（无 data）
- Source: `controller/user.go::UserUpdate`

### POST `/user/bind/school`

- 入参：`service.BindSchoolReq`（`school_id` + 学校账号 / 密码 / 验证码 等，参考具体学校的 form_fields）
- 响应：`ReplyOK`
- 副作用：service 调学校外部认证；已绑定再次绑定会失败
- Source: `controller/user.go::UserBindSchool`

### POST `/user/avatar` / POST `/user/background`

- 入参：`multipart/form-data`，字段名 **`file`**
- 响应：`data: { "url": "..." }`（OSS 完整 URL，可直接 `<img src=...>`）
- Source: `controller/user.go::UserUploadAvatar / UserUploadBackground`

### GET `/user/chat/unread`

订单聊天未读汇总。

- 响应：`data: { "total": <int>, "by_order": { "<order_id>": <int>, ... } }`（key 是订单 ID 字符串）
- Source: `controller/user.go::UserChatUnreadSummary`

### GET `/user/collects`

当前用户的收藏列表。

- Query：
  - `ext_type` (1–4)：必填语义；1 帖子 / 2 提问 / 3 回答 / 4 商品
  - `page` / `page_size`（也接受 `pageSize`）
- 响应：`data: { "list": [...], "total": <int>, "page": <int>, "page_size": <int> }`。`list` 内部结构按 ext_type 不同：
  - 1/2/3：enrich 后的 `response.ArticleWithAuthor`
  - 4：enrich 后的 good `map`
- 注意：商品分支**不按学校过滤**（避免未绑校用户收藏列表被筛空）
- Source: `controller/user.go::UserListCollects`

### GET `/user/:id` （UserProfile）

任意非删除用户的公开身份。

- 路径参数：`:id` 目标用户 ID
- 响应：`data: response.UserProfile`（精简版，仅公开字段）
- Source: `controller/user.go::UserProfile`

### GET `/user/:id/posts` / `/questions` / `/answers`（共用同一段逻辑）

按 `articleType` 列出某个用户发表的内容。

- 路径参数：`:id` 被查看的用户 ID
- Query：`page`、`pageSize`、可选 `sort=updated_at`
- 响应：`data: { "list": [response.ArticleWithAuthor], "total", "page", "page_size" }`
- 可见性：自己看自己含未发布 / 私密；看别人仅公开
- 注：因为这条路由没过 `LoadUserSchool`，**`viewerSchoolID = 0` 传到 DAO**，等价"未绑学校视角"——多数情况就是公开内容
- Source: `controller/user.go::UserListPosts / UserListQuestions / UserListAnswers`（统一委托 `UserListArticlesByType`）

### 收货地址 CRUD

| API | 入参 | 响应 |
|---|---|---|
| GET `/user/locations` | 无 | `data: { "list": [...] }` |
| POST `/user/locations` | `service.UserLocationCreateReq` | `data: { "id": <int> }` |
| PUT `/user/locations/:id` | `service.UserLocationUpdateReq` | `ReplyOK` |
| DELETE `/user/locations/:id` | 无 | `ReplyOK`（软删除） |
| POST `/user/locations/:id/default` | 无 | `ReplyOK` |

Source: `controller/user_location.go::UserLocation*`

## 关于 `LoadUserSchool` 的强调

整组 `/api/v1/user/...` 都在 `api.Use(middleware.LoadUserSchool())` 之前，所以 handler 里：

- `middleware.GetSchoolID(ctx)` 永远 0
- 但响应里的 `school_id` 来自 DB（`UserInfo` 直接读 user 表）
- service 层依然知道当前用户的 school_id（自己从 DB 读）

这个设计的原因：用户身份相关接口对学校隔离没需求（看自己 / 看别人公开主页），强行加 LoadUserSchool 反而徒增一次 DB 查询。
