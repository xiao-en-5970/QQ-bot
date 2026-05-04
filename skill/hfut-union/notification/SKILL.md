# notification — 站内通知

> 父 skill：`../SKILL.md`。点赞 / 评论 / 回复 / 官方通知都走这里。

## 路由表

| 方法 | 路径 | Handler | 用途 |
|---|---|---|---|
| GET | `/api/v1/notifications` | `NotificationList` | 列出我的通知 |
| GET | `/api/v1/notifications/unread_count` | `NotificationUnreadCount` | 未读数（按类型分） |
| POST | `/api/v1/notifications/read` | `NotificationMarkRead` | 标记已读（批量或全部） |

> 全部 JWT + LoadUserSchool（业务本身不依赖学校，但因为路由位置在中间件之后，会无副作用地过一次 LoadUserSchool）。

## 通知类型

类型枚举常量在 controller 里 `notificationVO`（**不是 `app/vo` 包**，是 controller 内部定义）。常见值：

| 类型 | 触发场景 |
|---|---|
| `like_article` | 我的帖子 / 提问 / 回答被点赞 |
| `like_comment` | 我的评论被点赞 |
| `comment` | 我的内容收到评论 |
| `reply` | 我的评论收到回复 |
| `official` | 官方推送（admin 发的公告） |

具体取值还是看 `app/controller/notification.go` 顶部的 `notificationVO` 结构 + 文档注释。

## 详细

### GET `/notifications`

- Query：
  - `type`：可选，按类型过滤
  - `page` / `page_size`
  - `only_unread`：`1` = 只看未读
- 响应：`data: { "list": [notificationVO...], "total", "page", "page_size" }`
- 每条 notification 含：
  - `id` / `created_at` / `read_at`（null = 未读）
  - `type`
  - `actor`（触发通知的人，`notificationAuthor` 结构）
  - `target`（被点赞 / 被评论的对象引用）
- Source: `controller/notification.go::NotificationList`

### GET `/notifications/unread_count`

- 入参：无
- 响应：`data: { "total": <int>, "by_type": { "<type>": <int>, ... } }`
- Source: `controller/notification.go::NotificationUnreadCount`

### POST `/notifications/read`

批量或全部标记已读。

- 入参：`controller.NotificationMarkReadReq`：
  ```json
  {
    "ids": [1, 2, 3],   // 批量标记
    "all": false        // 或者 all=true 把所有未读都标已读
  }
  ```
- 响应：`ReplyOK`
- Source: `controller/notification.go::NotificationMarkRead`

## 触发关系（哪些操作会产生通知？）

参考 `./interaction/SKILL.md` 末尾的"行为埋点 / 通知关系"表：

| 操作 | 通知 |
|---|---|
| 评论别人内容 | `comment` 通知给内容作者 |
| 回复别人评论 | `reply` 通知给被回复者 |
| 点赞文章 / 商品 | `like_article` 通知给作者 |
| 点赞评论 (extType=5) | `like_comment` 通知给评论作者 |
| 取消点赞 | **不发通知** |

官方通知（`official`）：admin 通过管理后台发出（具体接口看 `./admin/SKILL.md`，但截止目前 router 里没有看到独立的"管理员发送官方通知"接口——可能挂在某个其它 admin 接口下，或暂未开放）。

## 前端常见用法

```
启动 / 进首页：
  GET /notifications/unread_count   显示红点
轮询 / 长连接 / 用户点开通知中心：
  GET /notifications?only_unread=1  列出未读
用户点开某条 / 全部已读：
  POST /notifications/read with ids=[...] 或 all=true
```
