# admin — 管理后台

> 父 skill：`../SKILL.md`。**全部需要 `JWTAuth` + `AdminAuth`**——`AdminAuth` 中间件读 DB 校验 `user.Role >= RoleAdmin（2）` 且未禁用。

> JWT 里**不带 role**，所以 `AdminAuth` 每次都会查一次 user 表。先调 `POST /admin/login`（在 auth 模块）拿 token。

## 角色级别

| Role | 含义 |
|---|---|
| 1 | 普通用户 |
| 2 | 管理员（admin） |
| 3 | 超级管理员（super admin） |

`AdminAuth` 通过 `>= 2` 即放行。具体细分权限（比如哪些操作需 super）目前是 service 层各自实现。

## 路由组结构

```
/api/v1/admin/login                    auth 模块（公开）
/api/v1/admin/users/*                  用户管理
/api/v1/admin/posts/*                  帖子管理（articleType=1）
/api/v1/admin/questions/*              提问管理（articleType=2）
/api/v1/admin/answers/*                回答管理（articleType=3）
/api/v1/admin/schools/*                学校管理
/api/v1/admin/goods/*                  商品管理（全站）
/api/v1/admin/orders/*                 订单管理（全站）
/api/v1/admin/user-locations/*         用户收货地址管理
```

下面按业务分块列。

---

## 用户管理 (`/admin/users/*`)

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/admin/users` | `AdminUserList` | 列出用户（含禁用） |
| POST | `/admin/users` | `AdminUserCreate` | 管理员直接创建用户 |
| PUT | `/admin/users/:id` | `AdminUserUpdate` | 改用户（school_id / avatar / background 等） |
| DELETE | `/admin/users/:id` | `AdminUserDisable` | 禁用（软） |
| POST | `/admin/users/:id/restore` | `AdminUserRestore` | 恢复 |
| PUT | `/admin/users/:id/role` | `AdminUserUpdateRole` | 改角色（普通 / admin / super） |
| PUT | `/admin/users/:id/status` | `AdminUserUpdateStatus` | 改账号状态（启用 / 禁用） |

### 重点 API

- **AdminUserList**：Query `page` / `pageSize` / `status`（不传 = 含禁用）；密码字段在响应里被清空；URL 字段统一补 OSS 前缀
- **AdminUserCreate**：匿名 body：
  ```json
  {
    "username": "...",
    "password": "...",
    "school_id": 0,
    "role": 1,
    "status": 1
  }
  ```
  会校验 role / status 枚举
- **AdminUserUpdate**：匿名 body：`school_id` / `avatar` / `background`
  - `avatar` / `background` 字段会调 `oss.PathForStorage` **规范化**（如果给的是完整 URL，会反算成 OSS 内部 key 存库）
- **UpdateRole / UpdateStatus**：路径参数 `:id`，body 含目标值，会校验枚举

Source: `controller/admin.go` / `controller/admin_user.go` 等

---

## 文章管理（帖子 / 提问 / 回答 共用一套）

帖子 / 提问 / 回答的 admin 路由复用同一组 handler，靠路由 lambda 闭包传 `articleType` 区分：

| 方法 | 路径 | 真正调用 |
|---|---|---|
| GET | `/admin/posts` | `AdminArticleList(constant.ArticleTypeNormal)` |
| POST | `/admin/posts` | `AdminArticleCreate(constant.ArticleTypeNormal)` |
| PUT | `/admin/posts/:id` | `AdminArticleUpdate(constant.ArticleTypeNormal)` |
| DELETE | `/admin/posts/:id` | `AdminPostDisable` |
| POST | `/admin/posts/:id/restore` | `AdminPostRestore` |
| GET | `/admin/questions` | `AdminArticleList(constant.ArticleTypeQuestion)` |
| ... 同上模式 questions/answers | |

### 重点 API

- **AdminArticleList**：
  - Query：`include_invalid`（默认 1，**含已删除**；设 0 则只看正常）
  - 还可以传 `school_id` 限定学校
- **AdminArticleCreate**（注释明确）：
  - 创建时 `status=3`（草稿）
  - 回答（articleType=3）必须带 `parent_id`
  - **可以设 `school_id=0` 表全站公开**——这是 admin 才有的权限
- **AdminArticleUpdate**：
  - **含已删除的也可以改**（用于审核 / 恢复 / 修改后再恢复）
  - 可改 `status` / `images`（自动转存储路径）/ `user_id`（甚至能置空，把文章设为"无主"）
- **AdminPostDisable / Restore**：
  - 内部统一调 `adminArticleDisable`，注意此函数**不区分 articleType**（`articleType` 参数没用），只按 ID 软删
  - `Restore` 翻回正常状态

Source: `controller/admin.go::AdminArticle*` + `controller/article.go::adminArticleDisable`

---

## 学校管理 (`/admin/schools/*`)

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/admin/schools` | `AdminSchoolList` |
| POST | `/admin/schools` | `AdminSchoolCreate` |
| PUT | `/admin/schools/:id` | `AdminSchoolUpdate` |
| DELETE | `/admin/schools/:id` | `AdminSchoolDisable` |
| POST | `/admin/schools/:id/restore` | `AdminSchoolRestore` |

### 重点

- **AdminSchoolList**：`include_invalid` 含禁用学校
- **AdminSchoolCreate**：默认 `form_fields` 配置（含学号 / 密码两项）
- **AdminSchoolUpdate**：注释提到 `eam_url` / `info_url` 等敏感 URL 仅 DB 可改（也就是必须经过 admin 接口）
- 状态：`status` 字段控制学校是否在普通用户列表里出现（`SchoolListForBind` 只列 `status=valid`）

Source: `controller/admin.go::AdminSchool*`

---

## 商品管理 (`/admin/goods/*`)

全站维度，**绕过学校隔离**——admin 看到全部学校的商品。

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/admin/goods` | `AdminGoodList` |
| GET | `/admin/goods/:id` | `AdminGoodGet` |
| POST | `/admin/goods` | `AdminGoodCreate` |
| PUT | `/admin/goods/:id` | `AdminGoodUpdate` |
| POST | `/admin/goods/:id/publish` | `AdminGoodPublish` |
| POST | `/admin/goods/:id/off-shelf` | `AdminGoodOffShelf` |
| POST | `/admin/goods/:id/images` | `AdminGoodUploadImages` |
| DELETE | `/admin/goods/:id` | `AdminGoodDisable`（软删） |
| POST | `/admin/goods/:id/restore` | `AdminGoodRestore` |

### 重点

- **AdminGoodList**：Query `school_id`（限定学校；不传 = 全站）/ `include_invalid`
- **AdminGoodDisable / Restore**：内部走 `AdminUpdate` 改 `status` 字段实现（不删行）

Source: `controller/admin_good.go`

---

## 订单管理 (`/admin/orders/*`)

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/admin/orders` | `AdminOrderList` |
| GET | `/admin/orders/:id` | `AdminOrderGet` |
| GET | `/admin/orders/:id/messages` | `AdminOrderMessages` |

### 重点

- **AdminOrderList**：Query `school_id` / `include_invalid`
- **AdminOrderGet**：跟 buyer / seller 接口一样返回 `orderToMap`，但**绕过 buyer/seller 校验**
- **AdminOrderMessages**：调 `ListOrderMessagesAdmin`，**不校验请求方是否在订单参与人里**——admin 可以读任何订单的聊天记录（处理纠纷用）

Source: `controller/admin_order.go`

---

## 用户收货地址管理 (`/admin/user-locations/*`)

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/admin/user-locations` | `AdminUserLocationList` |
| POST | `/admin/user-locations` | `AdminUserLocationCreate` |
| DELETE | `/admin/user-locations/:id` | `AdminUserLocationDelete` |

### 重点

- **List**：Query `user_id`（按用户过滤）/ `all_status`（含已删的）；结果附上每个地址的 `username`
- **Create**：匿名 body → `service.UserLocationCreateReq`（admin 可以替任何用户加地址，需带 `user_id`）
- **Delete**：软删

Source: `controller/admin_user_location.go`

---

## 关于"软删 / 状态字段"统一约定

后端约定**不真正 DELETE 行**，所有删除操作都把 `status`（或类似字段）置为禁用值。`Restore` 接口翻回去。所以：

- 普通用户接口列表里看不到的内容，admin 用 `include_invalid=1` 都能看到
- `Disable` 不会丢数据；`Restore` 是无损还原
- 真正物理删除的能力**没有暴露在 HTTP API**（要的话直接连 DB）

## 缺失的能力（agent 需要时别瞎找）

router.go 里截止本 skill 写就时**没有**的接口：

- 发送官方通知（push 给所有用户的"`type=official`"通知）—— 当前未公开 API
- 修改用户密码（用户自己改的接口、admin 改用户密码的接口都没看到）
- 强制踢用户下线（JWT 没黑名单机制；`/user/logout` 是空实现）

如果 agent 被问到这些，应**明确告诉用户暂未实现**而不是编造路径。
