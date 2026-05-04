# post — 帖子（articleType=1）

> 父 skill：`../SKILL.md`。**post / question / answer 共用同一组 `ArticleHandlers`**，只是绑定的 `articleType` 不同。

> 所有路由都在 `LoadUserSchool` 之后，自带学校隔离。

## 路由表

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/api/v1/post` | `PostHandlers.List` | 帖子列表（支持推荐排序） |
| GET | `/api/v1/post/drafts` | `PostHandlers.ListDrafts` | 我的草稿列表 |
| GET | `/api/v1/post/search` | `PostHandlers.Search` | 搜帖子（仅本类型） |
| POST | `/api/v1/post` | `PostHandlers.Create` | 新建帖子（默认草稿） |
| GET | `/api/v1/post/:id` | `PostHandlers.Get` | 帖子详情 |
| PUT | `/api/v1/post/:id` | `PostHandlers.Update` | 改帖子 |
| DELETE | `/api/v1/post/:id` | `PostHandlers.Delete` | 软删 |
| POST | `/api/v1/post/:id/images` | `PostHandlers.UploadImages` | 给帖子加图（multipart） |
| POST | `/api/v1/post/:id/publish` | `PostHandlers.Publish` | 草稿发布上线 |

## 详细

### GET `/post`

- Query：
  - `page`、`pageSize`
  - `sort`：可选 `updated_at`（默认）/ `recommend`（**推荐链路**）
  - `refresh_token`：仅 `sort=recommend` 时使用，刷新时把上次响应里的 `refresh_token` 带回来；首次不传
- 响应：`data: { "list": [response.ArticleWithAuthor], "total", "page", "page_size" }`；`sort=recommend` 时多 `refresh_token` 和 `sort` 字段
- 副作用：非推荐列表会调 `stampArticlesViewedBatch` 做浏览埋点
- Source: `controller/article.go::ArticleHandlers.List`

### GET `/post/drafts`

我的草稿。

- Query：`page`、`pageSize`
- 响应：同 List
- 注：草稿不参与浏览埋点
- Source: `ArticleHandlers.ListDrafts`

### GET `/post/search?q=...`

仅在帖子里搜。

- Query：`q`（关键词）、`page`、`pageSize`、`sort`
- 响应：同 List
- 副作用：浏览埋点
- 想跨内容类型搜（帖子+提问+回答）→ 用全局搜索 `./search/SKILL.md`
- Source: `ArticleHandlers.Search`

### POST `/post`

新建帖子（创建后默认是草稿，需要再调 `/publish` 才上线）。

- 入参：`service.CreateArticleReq`（`title` / `content` / `images` / `visibility` 等）
- 响应：`data: { "id": <int> }`
- 副作用：当前用户**未绑定学校**会业务错；帖子类型不接受 `parent_id`
- Source: `ArticleHandlers.Create`

### GET `/post/:id`

帖子详情。

- 路径参数：`:id`
- 响应：`data: response.ArticleWithAuthor`
- 副作用：已发布且 `publish_status=2` 时调 `RecordBehavior(View)` 行为埋点（用于推荐召回）
- Source: `ArticleHandlers.Get`

### PUT `/post/:id`

- 入参：`service.UpdateArticleReq`
- 响应：`ReplyOK`
- Source: `ArticleHandlers.Update`

### DELETE `/post/:id`

软删除（不会真删行）。

### POST `/post/:id/images`

multipart 上传，字段名 **`files`**（复数）。

- 响应：`data: { "urls": [...] }`
- 副作用：把 URL 关联到这篇帖子的图集
- Source: `ArticleHandlers.UploadImages`

### POST `/post/:id/publish`

把草稿发布上线。

- 入参：无
- 响应：`ReplyOK`
- 副作用：service 层 `PublishDraft` 会校验草稿归属当前用户
- Source: `ArticleHandlers.Publish`

## 关键 VO 引用

- `service.CreateArticleReq` / `UpdateArticleReq` 在 `app/service/article.go`
- `response.ArticleWithAuthor` 在 `app/vo/response/article.go`（含作者基本信息 + 文章本体 + 互动计数）

## 跟 question / answer 的差异

- post 创建时**不带 `parent_id`**
- post 不能有"挂在某 question 下"的概念
- 路由前缀 `/post`；question 是 `/question`；answer 是 `/answer`，互相不通用

具体到 question / answer 的差异看各自的 SKILL.md。
