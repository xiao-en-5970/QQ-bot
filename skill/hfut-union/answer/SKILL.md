# answer — 回答（articleType=3）

> 父 skill：`../SKILL.md`。本模块和 `./post/SKILL.md` 共用 `ArticleHandlers`，绝大多数行为一致，下面只列**差异点**——通用语义请直接看 post 那份。

## 路由表

| 方法 | 路径 | Handler | 备注 |
|---|---|---|---|
| GET | `/api/v1/answer` | `controller.AnswerListWithParent` | **不是**原 `AnswerHandlers.List`，而是带 `parent_question` 的特化版 |
| GET | `/api/v1/answer/drafts` | `AnswerHandlers.ListDrafts` | 我的回答草稿 |
| GET | `/api/v1/answer/search` | `AnswerHandlers.Search` | 搜回答 |
| POST | `/api/v1/answer` | `AnswerHandlers.Create` | 新建回答（**必须带 `parent_id`**） |
| GET | `/api/v1/answer/:id` | `controller.AnswerGetWithParent` | **特化版**：详情附 `parent_question` |
| PUT | `/api/v1/answer/:id` | `AnswerHandlers.Update` | 改回答 |
| DELETE | `/api/v1/answer/:id` | `AnswerHandlers.Delete` | 软删 |
| POST | `/api/v1/answer/:id/images` | `AnswerHandlers.UploadImages` | 加图 |
| POST | `/api/v1/answer/:id/publish` | `AnswerHandlers.Publish` | 发布 |

> 关键：`GET /answer` 和 `GET /answer/:id` 用的是 **`AnswerListWithParent` / `AnswerGetWithParent`**（独立 handler），返回 `response.AnswerWithAuthor` 而不是 `ArticleWithAuthor`，多了 `parent_question` 字段方便社区流 / 详情页展示"这是回答的什么问题"。

## 跟 post / question 的差异

1. **`articleType=3`**
2. **`POST /answer` 创建时必须带 `parent_id`**（提问 ID），service 层会校验
3. **`GET /answer`** 用的是社区流特化版，返回包 `parent_question`
4. **`GET /answer/:id`** 同上特化版
5. List/Search/ListDrafts 等仍然走原 `AnswerHandlers.*`，返回普通 `ArticleWithAuthor`（无 parent）

## 详细：特化的两个 handler

### GET `/answer`（AnswerListWithParent）

社区流式列出所有回答，每条带它属于哪个提问。

- Query：`page`、`pageSize`、`sort=updated_at`/`recommend`、`refresh_token`（仅推荐）
- 响应：`data: { "list": [response.AnswerWithAuthor], "total", "page", "page_size" }`；`sort=recommend` 时多 `refresh_token`
- `AnswerWithAuthor` 比 `ArticleWithAuthor` 多一个 `parent_question` 嵌套对象（**仅 ID + 标题摘要**，不含完整 content）
- 副作用：`stampAnswersViewedBatch` 浏览埋点（针对 answer）；推荐链路走推荐召回
- Source: `controller/answer_feed.go::AnswerListWithParent`

### GET `/answer/:id`（AnswerGetWithParent）

回答详情，附父提问。

- 路径参数：`:id` 回答 ID
- 响应：`data: response.AnswerWithAuthor`
- 副作用：已发布且公开时调 `RecordBehavior(View)`
- Source: `controller/answer_feed.go::AnswerGetWithParent`

## POST `/answer` 创建时

```json
{
  "parent_id": 123,        // ← 必填，提问 ID
  "content": "...",
  "images": [...],
  "visibility": ...
}
```

`parent_id` 缺失或对应的提问不存在 / 不属于本校时，service 层报错。

## 通用语义参考 post

详细的列表参数 / refresh_token 用法 / 草稿三段式 / 学校隔离 → `./post/SKILL.md`。
