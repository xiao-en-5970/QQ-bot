# oss — 对象存储 GET / Upload / Delete

> 父 skill：`../SKILL.md`。对象存储统一接口，**`GET` 公开**（前端 `<img src=...>` 直接用），**Upload / Delete 需 JWT 且有路径权限校验**。

## 路由表

| 方法 | 路径 | 鉴权 | Handler | 用途 |
|---|---|---|---|---|
| GET | `/api/v1/oss/*path` | 公开 | `controller.OSSGet` | 读取对象（图片 / 附件） |
| POST | `/api/v1/oss/*path` | JWT | `controller.OSSUpload` | 上传 |
| DELETE | `/api/v1/oss/*path` | JWT | `controller.OSSDelete` | 删除 |

## 详细

### GET `/api/v1/oss/*path`

- 路径参数：`*path` 通配 OSS 内部对象路径
- **响应不走 JSON 信封**：
  - 成功：直接返回文件流（用 `c.File`）
  - 失败：裸 `404`，无 `code: ..., message: ..., data: ...` 信封
- 鉴权：**公开**，前端可以直接 `<img src="https://api.example.com/api/v1/oss/avatar/123.jpg">` 嵌入
- Source: `controller/oss.go::OSSGet`

### POST `/api/v1/oss/*path`

上传文件。

- 路径参数：`*path` 目标 OSS 路径
  - 如果 path 以 `/` 结尾（例如 `user/123/`），handler 会自动用上传文件的原 `Filename` 拼到末尾
  - 否则 path 自己就是完整对象 key
- 入参：`multipart/form-data`，字段名 `file`
- 响应：JSON 信封 `data: { "url": "..." }`
- Source: `controller/oss.go::OSSUpload`

### DELETE `/api/v1/oss/*path`

- 路径参数：`*path` 要删的对象 key
- 响应：JSON 信封
- Source: `controller/oss.go::OSSDelete`

## 路径权限规则（`ossCheckPathPermission`）

Upload / Delete 时按 path 前缀做权限校验：

| 路径前缀 | 谁能改 |
|---|---|
| `user/{id}/...` | 仅 `id == 当前用户` 本人能上传 / 删 |
| `article/{id}/...` | 仅文章所有者本人 |
| `school/{id}/...` | **仅管理员** |
| 其它前缀 | 看 service 层 / 默认拒绝（建议查源码） |

权限不通过 → 返回 403。

## 跟其它接口的关系

业务模块自己暴露了"上传图片"接口，本质都是对 OSS 的封装：

| 业务接口 | 内部最终调用 OSS |
|---|---|
| `POST /user/avatar` | `user/{id}/avatar.{ext}` |
| `POST /user/background` | `user/{id}/bg.{ext}` |
| `POST /post/:id/images` 等 | `article/{id}/imgs/...` |
| `POST /goods/:id/images` | `article/{id}/imgs/...`（商品也走 article 命名空间，看具体配置） |

**前端推荐用业务接口**（如 `/user/avatar`）而不是直接打 OSS——业务接口会自动绑定关系（更新 user.avatar 字段、关联文章图集等），直接打 OSS 只完成存储，没有副作用绑定。

## 文件大小 / 类型限制

具体 size / mime 限制看 service 层 `oss.go` 配置（一般 10–20 MB 限制 + 允许 jpg/png/webp/gif）。超限会被业务错误挡住。
