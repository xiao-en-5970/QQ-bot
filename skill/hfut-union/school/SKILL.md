# school — 学校列表 / 详情 / 验证码（绑定学校时用）

> 父 skill：`../SKILL.md`。

## 路由表

| 方法 | 路径 | 鉴权 | Handler | 用途 |
|---|---|---|---|---|
| GET | `/api/v1/schools` | JWT | `controller.SchoolListForBind` | 列出可绑定的学校（精简字段） |
| GET | `/api/v1/schools/:id` | JWT | `controller.SchoolDetailForBind` | 学校详情（含绑定表单字段） |
| GET | `/api/v1/schools/:id/captcha` | JWT | `controller.SchoolCaptcha` | 拿学校登录验证码图片 |

> 这 3 个路由都在 `LoadUserSchool` 之前注册，handler 里没有 school 上下文（也用不到）。

## 详细

### GET `/schools`

列出可绑定的学校列表，**精简字段**（只 `id`/`name`/`code`，不含 `form_fields`）。

- 入参：无
- 响应：`data: { "list": [{ "id", "name", "code" }, ...], "total": <int> }`
- 过滤：`status=valid` 且 `code` 非空
- Source: `controller/school.go::SchoolListForBind`

### GET `/schools/:id`

学校详情，**含绑定要用到的 `form_fields`、`captcha_url`、`login_url`**——前端用来渲染绑定表单。

- 路径参数：`:id` 学校 ID
- 响应：`data` 是扁平 map：
  ```json
  {
    "id": 1,
    "name": "...",
    "code": "...",
    "form_fields": [...],     // 绑定表单需要哪些字段
    "captcha_url": "...",     // 学校验证码源 URL
    "login_url": "..."        // 学校登录 URL
  }
  ```
- Source: `controller/school.go::SchoolDetailForBind`

### GET `/schools/:id/captcha`

拿学校登录验证码图片 + token。

- 路径参数：`:id` 学校 ID
- 响应：`data: { "image": "<base64 PNG>", "token": "<captcha_token>" }`
- 调用：内部走 `schools.GetCaptcha(schoolID)`，外部源是各学校自己的认证站点
- 副作用：会去外部学校站点拉验证码；学校 `form_fields` / `captcha_url` / `login_url` 任一不全会返回错误
- Source: `controller/school.go::SchoolCaptcha`

## 典型调用链（绑定流程）

```
1. GET /api/v1/schools                    用户从下拉列表选学校
2. GET /api/v1/schools/:id                按选择拉绑定表单要哪些字段
3. （部分学校）GET /api/v1/schools/:id/captcha  → 显示验证码图给用户
4. POST /api/v1/user/school-login         填写 username/password/captcha + token，验证身份
5. POST /api/v1/user/bind/school          验证成功后才能真正绑定（user 模块）
```

中间任何一步失败，可以回到步骤 3 重新刷验证码（每个 captcha_token 一次性）。
