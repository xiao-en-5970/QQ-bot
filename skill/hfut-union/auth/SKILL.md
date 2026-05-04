# auth — 登录 / 注册 / 登出

> 父 skill：`../SKILL.md`（全局信封 / JWT 约定从那里读）。

## 路由表

| 方法 | 路径 | 鉴权 | Handler | 一句话 |
|---|---|---|---|---|
| POST | `/api/v1/user/login` | 公开 | `controller.UserLogin` | 普通用户名密码登录（返回 JWT） |
| POST | `/api/v1/user/school-login` | 公开 | `controller.UserSchoolLogin` | 学校 CAS 登录验证（**不发 JWT**，只验身份） |
| POST | `/api/v1/user/register` | 公开 | `controller.UserRegister` | 注册 |
| POST | `/api/v1/admin/login` | 公开 | `controller.AdminLogin` | 管理员登录（仅 `role>=2`） |
| GET | `/api/v1/user/logout` | JWT | `controller.UserLogout` | 登出（**当前为空实现**，前端只需丢 token） |

## 详细

### POST `/api/v1/user/login`

普通用户登录。

- Body: `{ "username": "...", "password": "..." }`
- 响应：信封 `data` 是 **JWT 字符串**（不是 object，是裸字符串）；`message="登录成功"`
- Source: `controller/user.go::UserLogin`
- 副作用：禁止与 `OrderOfficialUsername`（订单官方账号）同名登录，service 层会拦截

### POST `/api/v1/user/school-login`

学校端登录——对接学校 CAS。验证学校账号是否真的属于这个学生，**不返回 token**。一般跟 `/user/bind/school` 配合使用。

- Body：`{ "school_code": "...", "username": "...", "password": "...", "captcha": "...", "captcha_token": "..." }`
- 部分学校需要验证码，先调 `GET /api/v1/schools/:id/captcha`（在 school 模块）拿到 image+token 再带过来
- 响应：`data: { "student_id": "...", "name": "..." }`
- Source: `controller/user.go::UserSchoolLogin`
- 依赖：`schools.Login`（学校适配器，可能向校外认证站点拉数据）

### POST `/api/v1/user/register`

- Body：`{ "username": "...", "password": "...", "re_password": "..." }`
- 响应：`data: { "user_id": <int> }`
- 校验：两次密码必须一致
- Source: `controller/user.go::UserRegister`

### POST `/api/v1/admin/login`

管理员账号密码登录。

- Body：`{ "username": "...", "password": "..." }`
- 响应：`data: { "token": "..." }`（注意：这里 `data` 是 object 包了 token；跟 `/user/login` 直接发字符串的形态**不一样**，两者不要搞混）
- Source: `controller/admin.go::AdminLogin`
- 副作用：服务层 `AdminLogin` 校验 `user.Role >= RoleAdmin（2）` + 未禁用

### GET `/api/v1/user/logout`

当前是空实现（`func UserLogout(ctx) { return }`），前端只要把本地 token 丢掉就行。后端没有黑名单 / 主动失效机制。

## JWT claims（再确认一下，避免 agent 误以为里面有 role）

```json
{
  "user_id": 123,
  "username": "alice",
  "exp": 1234567890,
  "iss": "HFUT-Graduation-Project"
}
```

`role` / `school_id` / `email` **都不在 token 里**——需要用就读 DB（`dao.User().GetByID`）。

## 调用顺序（前端常见集成）

```
1. POST /user/register                 创建账号
2. POST /user/login                    拿 token
3. （可选）GET /schools                列学校
   POST /user/school-login             拿到学生信息验证身份
   POST /user/bind/school              把学校跟当前账号绑定（user 模块）
4. 之后请求都带 Authorization: Bearer <token>
```
