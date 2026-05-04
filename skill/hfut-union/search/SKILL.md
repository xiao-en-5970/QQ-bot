# search — 跨内容类型聚合搜索

> 父 skill：`../SKILL.md`。这是**跨 article 类型**的搜索（一次返回帖子 + 提问 + 回答）；如果只想搜某一类，去对应模块自己的 `/search` 接口。

## 路由表

| 方法 | 路径 | 鉴权 | Handler | 用途 |
|---|---|---|---|---|
| GET | `/api/v1/search/articles` | JWT + LoadUserSchool | `controller.SearchArticles` | 跨 type 聚合搜索 |

## 详细

### GET `/search/articles`

聚合搜索接口，比单类型 `/post/search`、`/question/search`、`/answer/search` 更通用。

#### Query 参数

| 参数 | 必选 | 说明 |
|---|---|---|
| `q` | 推荐填 | 关键词（不填的话相当于"列出全部"，配合 `time_range` / `sort` 用作筛选） |
| `type` | 否 | 限定 articleType；不传 = 全部三类。可传 1（帖子）/ 2（提问）/ 3（回答），或多个用逗号分隔 |
| `visibility` | 否 | 可见性过滤（具体取值看 `controller/search.go` 注释里的常量） |
| `time_range` | 否 | 时间区间 preset：`day` / `week` / `month` / `all` 等 |
| `created_after` | 否 | ISO8601 时间下界 |
| `created_before` | 否 | ISO8601 时间上界 |
| `sort` | 否 | 排序：`relevance`（默认）/ `created_at` / `updated_at` / `views` / `likes` 等（具体取值看 controller 注释） |
| `page` | 否 | 默认 1 |
| `page_size` | 否 | 默认 10–20 |

#### 响应

```json
{
  "code": 200,
  "message": "...",
  "data": {
    "list": [response.ArticleWithAuthor, ...],
    "total": <int>,
    "page": <int>,
    "page_size": <int>
  }
}
```

`list` 里**混合**了帖子 / 提问 / 回答，每条用 `article_type` 字段区分。

#### 副作用

- 当 `q` 非空时调 `RecordBehavior(Search)` 埋点
- 调 `stampArticlesViewedBatchMixed` 给混合类型一起做浏览埋点
- service 层 `AggregateSearch` 按 `ViewerSchool=GetSchoolID(ctx)` 做学校隔离

#### Source

`controller/search.go::SearchArticles`

## 跟其他搜索的关系

| 范围 | 用什么 |
|---|---|
| 只搜帖子 | `GET /api/v1/post/search?q=...`（`./post/SKILL.md`）|
| 只搜提问 | `GET /api/v1/question/search?q=...`（`./question/SKILL.md`）|
| 只搜回答 | `GET /api/v1/answer/search?q=...`（`./answer/SKILL.md`）|
| 三类都搜 / 还要时间筛选 | **本接口** |
| 搜商品 | `GET /api/v1/goods?q=...`（`./good/SKILL.md`）|

## 使用建议

- 前端"全站搜索框"用本接口
- 内容流式 feed 不要用本接口；应该用 `/post`、`/answer` 各自的 `sort=recommend` 链路（推荐召回）
