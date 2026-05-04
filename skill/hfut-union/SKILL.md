# HFUT-Union Backend API（顶层索引）

> Skill 入口：要让 agent 调用 HFUT-Union 后端的任何能力，**先读这一份**，再按下面的"模块清单"按需 read 子 skill。**别一次把所有子 skill 都读进来**——progressive disclosure 的关键是上下文按需注入。

> **关于 bot 自身的行为**（什么时候该回复 / 群白名单 / 个性约束等）请读同级 `../bot/SKILL.md`。这一份只讲后端 API。

---

## 这是什么

合工大联盟（HFUT-Union）项目后端，Gin + GORM 实现的多模块 Web API。前端项目位于 `hfut-front`（别名 HFUT-Union），后端项目仓库 `go-proj/private/HFUT-Graduation-Project`。

业务范围：用户身份/学校认证、社区内容（帖子/提问/回答）、互动（评论/收藏/点赞）、二手商品+有偿求助交易、站内通知、对象存储、地图、管理后台。

## Source 路径（agent 需要更深资料时去这里）

* 路由表（最权威的"有哪些 API"）：`app/router/router.go`
* 各业务 Controller：`app/controller/*.go`
* 服务层（业务逻辑 / 校验）：`app/service/*.go`
* 入参 / 出参 VO：`app/vo/`、`app/vo/response/`
* 中间件（鉴权/学校加载）：`app/middleware/jwt.go`
* 错误码 / 信封封装：`package/reply/`、`package/errcode/`、`app/vo/response/response.go`
* 常量（ExtType / ArticleType / Status）：`package/constant/types.go`

---

## 全局约定（所有模块共用，不要在子 skill 里重复读这块）

### URL 前缀

所有业务接口都在 `/api/v1` 之下。子 skill 里的路径都已带 `/api/v1` 前缀。

### 返回信封（统一 JSON 格式）

```json
{
  "code": 200,
  "message": "...",
  "data": {...}
}
```

- 成功 `code = 200`（注意是 200 不是 0；`errcode.Success = 200`）。
- 失败也走相同信封，`code` 是业务错误码、`message` 是用户可读提示，`data` 通常省略。
- 某些 helper 名称：`reply.ReplyOK` / `ReplyOKWithData` / `ReplyErr` / `ReplyErrWithMessage` / `ReplyInvalidParams` / `ReplyUnauthorized` / `ReplyForbidden` / `ReplyNotFound` / `ReplyInternalError` 等。**包名是 `ReplyErr`，没有 `ReplyError`**。
- **三个不走信封的特例**：
  1. `OSSGet` 直接返回文件流或裸 404
  2. `MapTileProxy` 返回瓦片二进制 / 502 / 503 等裸状态
  3. `JWTAuth` 中间件失败时是裸 `code: 401`（数值）+ `Authorization` 头不通过

### 鉴权（JWT）

- Header：`Authorization: Bearer <token>`
- Token 通过 `/user/login` / `/user/school-login` / `/admin/login` 拿到（注意 `school-login` **不发 token**，只验证学校身份）
- Claims 只包含：`user_id`、`username` + 标准 RegisteredClaims（exp / Issuer="HFUT-Graduation-Project"）
- **JWT 里没有 role 也没有 school_id**——管理员判定 / 学校归属都是中间件读 DB 决定的

权限分三档：

| 档位 | 中间件 | 说明 |
|---|---|---|
| 公开 | 无 | `/user/login` / `/register` / OSS GET 等 |
| 普通 JWT | `JWTAuth` | 大部分用户接口 |
| Admin JWT | `JWTAuth` + `AdminAuth` | `user.Role >= RoleAdmin（2）` 才能进 |

### 学校隔离（`LoadUserSchool` 中间件）

`router.go` 在某一行 `api.Use(middleware.LoadUserSchool())` 之后注册的路由才有学校隔离。**该行之前注册的路由**（`/schools`、整个 `/user/...` 组、`/user/logout` 等）**没有学校上下文**——`middleware.GetSchoolID(ctx)` 在那些 handler 里恒为 0。

中间件内部行为：

```go
// 不读 JWT，只查库拿当前用户的 SchoolID 写到 ctx
user := dao.User.GetByID(userID)
ctx.Set("school_id", user.SchoolID)
```

数据过滤是各 service / dao 自己拿这个值做条件（如 `GetByIDWithSchoolOrPublicAndType` / `AggregateSearch` 的 `ViewerSchool`）。`school_id == 0`（未绑学校）时多数地方的语义是"仅看公开内容"。

### `ExtType` / `ArticleType` 常量

定义在 `package/constant/types.go`：

| 常量 | 值 | 含义 |
|---|---|---|
| `ArticleTypeNormal` | 1 | 帖子 |
| `ArticleTypeQuestion` | 2 | 提问 |
| `ArticleTypeAnswer` | 3 | 回答 |
| `ExtTypePost` | 1 | 帖子（被评论 / 收藏 / 点赞的对象类型） |
| `ExtTypeQuestion` | 2 | 提问 |
| `ExtTypeAnswer` | 3 | 回答 |
| `ExtTypeGood` | 4 | 商品（仅参与"收藏"和"评论"，不参与"点赞"是历史问题；以代码 `validCommentExtTypes` / `validLikeExtTypes` 为准） |
| `ExtTypeComment` | 5 | 评论（仅"点赞"用） |

---

## 模块清单

按业务关系分组。需要哪个就 read 哪个。

### 1. 身份与账户

| 模块 | 一句话用途 | 子 skill |
|---|---|---|
| **auth** | 登录、注册、登出（公开 + JWT 边界） | `./auth/SKILL.md` |
| **user** | 当前用户资料、收货地址、个人主页（被他人查看） | `./user/SKILL.md` |
| **school** | 列出可绑定学校 + 学校验证码 | `./school/SKILL.md` |

### 2. 三大内容类型

| 模块 | 一句话用途 | 子 skill |
|---|---|---|
| **post** | 帖子 CRUD + 草稿 + 列表/搜索 + 推荐 | `./post/SKILL.md` |
| **question** | 提问 CRUD + 看某提问下所有回答 | `./question/SKILL.md` |
| **answer** | 回答 CRUD（须挂在某 question 下） | `./answer/SKILL.md` |

> 这 3 类共用 `ArticleHandlers`（同一组 handler，靠 `articleType` 区分）。各自的 SKILL.md 只列差异点 + 路由前缀。

### 3. 通用互动 / 搜索

| 模块 | 一句话用途 | 子 skill |
|---|---|---|
| **interaction** | 评论 / 收藏（含收藏夹）/ 点赞，统一接口靠 `extType` 区分目标对象 | `./interaction/SKILL.md` |
| **search** | 跨内容类型聚合搜索 | `./search/SKILL.md` |

### 4. 交易

| 模块 | 一句话用途 | 子 skill |
|---|---|---|
| **good** | 商品（含二手 + 有偿求助）CRUD + 上下架 | `./good/SKILL.md` |
| **order** | 订单状态机 + 买卖双方聊天（不经手资金） | `./order/SKILL.md` |

### 5. 通用基础设施

| 模块 | 一句话用途 | 子 skill |
|---|---|---|
| **notification** | 站内通知（点赞/评论/回复/官方） | `./notification/SKILL.md` |
| **map** | 地图配置 + 瓦片代理（Martin） | `./map/SKILL.md` |
| **oss** | 对象存储 GET / Upload / Delete | `./oss/SKILL.md` |

### 6. 管理后台

| 模块 | 一句话用途 | 子 skill |
|---|---|---|
| **admin** | 全套管理员接口（用户/文章/学校/商品/订单/收货地址）需 `role>=2` | `./admin/SKILL.md` |

---

## 使用建议（给 agent）

1. 用户问"怎么登录" → 读 `./auth/SKILL.md`
2. 用户问"怎么发帖子" → 读 `./post/SKILL.md`
3. 用户问"商品怎么下单 + 怎么聊天" → 一次读 `./good/SKILL.md` + `./order/SKILL.md`
4. 用户问"管理员怎么删用户" → 读 `./admin/SKILL.md`
5. 用户问"评论怎么发" → 读 `./interaction/SKILL.md`（带 `extType` 用法）

每个子 skill 文件里的 API 都附带：
- 完整 HTTP method + 路径
- 鉴权要求（公开 / JWT / Admin）
- 入参（VO 名 / Query / form）
- 响应（VO 名 / 关键字段）
- 路径参数含义
- 副作用（行为埋点 / 通知推送 / 库存变更等）
- Source 引用（`controller/xxx.go::FuncName`），需要更深时直接跳过去
