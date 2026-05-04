# question — 提问（articleType=2）

> 父 skill：`../SKILL.md`。本模块和 `./post/SKILL.md` 共用 `ArticleHandlers`，绝大多数行为一致，下面只列**差异点**——通用语义请直接看 post 那份。

## 路由表

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/api/v1/question` | `QuestionHandlers.List` | 提问列表 |
| GET | `/api/v1/question/drafts` | `QuestionHandlers.ListDrafts` | 我的提问草稿 |
| GET | `/api/v1/question/search` | `QuestionHandlers.Search` | 搜提问 |
| POST | `/api/v1/question` | `QuestionHandlers.Create` | 新建提问（草稿） |
| GET | `/api/v1/question/:id/answers` | `controller.QuestionListAnswers` | **该提问下的所有回答**（独立 handler） |
| GET | `/api/v1/question/:id` | `QuestionHandlers.Get` | 提问详情 |
| PUT | `/api/v1/question/:id` | `QuestionHandlers.Update` | 改提问 |
| DELETE | `/api/v1/question/:id` | `QuestionHandlers.Delete` | 软删 |
| POST | `/api/v1/question/:id/images` | `QuestionHandlers.UploadImages` | 加图 |
| POST | `/api/v1/question/:id/publish` | `QuestionHandlers.Publish` | 发布 |

> **注意路由顺序**：`/question/:id/answers` 注册在 `/question/:id` 之前，gin 才能正确分流，别在前端混着请求。

## 跟 post 的差异

1. **`articleType=2`**——查 / 列出来的都是 type=2 的文章，post 接口拿不到
2. 创建提问也**不带 `parent_id`**（区别于 answer）
3. 多了一条 `GET /question/:id/answers` 列出该提问下所有回答的接口（看下面）

## 唯一的特别接口：GET `/question/:id/answers`

列出某提问下的所有回答。

- 路径参数：`:id` 提问 ID
- Query：`page`、`pageSize`
- 响应：`data: { "list": [response.ArticleWithAuthor], "total", ... }`
  - 注意：返回的是 **`ArticleWithAuthor`**（**不是 `AnswerWithAuthor`**）；没有 `parent_question` 嵌套（因为本来就在 question 详情页里看，不需要重复展示父）
- 副作用：调 `stampArticlesViewedBatch` 给回答做浏览埋点
- 错误：提问不存在 / 不属于本校时返回业务错
- Source: `controller/answer_feed.go::QuestionListAnswers`

## 通用语义

参考 `./post/SKILL.md`：
- 列表参数 / 推荐排序 / refresh_token 完全一样
- 草稿创建 → 上传图 → 发布的三段式流程一致
- 浏览埋点行为一致
- 学校隔离规则一致
