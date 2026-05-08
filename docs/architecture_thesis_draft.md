# HFUT Union 与 QQ 智能体系统架构说明（论文初稿素材）

> 本文档基于当前三个代码仓的实现整理：`HFUT-Graduation-Project`（后端）、`hfut-front/HFUTUnion`（React Native 前端）、`QQ-bot`（QQ 智能体）。内容覆盖目前项目中已经出现或接入过的技术：移动端、HTTP API、QQ Bot、NapCat/OneBot11、Kimi/Moonshot、PostgreSQL、Redis、OSS/Qiniu、MapLibre、Martin 瓦片、GraphHopper、JWT、WebSocket 等。

## 1. 项目总体定位

本项目面向高校学生的校园互助与二手交易场景，核心目标是把 App 内的结构化信息流与 QQ 群的即时讨论场景连接起来。系统由三类用户入口组成：

- 移动端 App：用于浏览、发布和管理帖子、求解答、求物品、二手商品、订单与通知。
- QQ 群：学生可以自然语言发布商品、求物品、提问、回答、下架等，QQ-bot 识别后同步到后端。
- 管理 / 运维入口：后端提供管理员 API；QQ-bot 可把群接入申请、上架事件转发到内部运维群。

从架构上看，移动端负责用户交互，HFUT 后端负责业务规则、持久化和服务集成，QQ-bot 负责把 QQ 群消息转成后端结构化数据，地图服务负责位置选择和路线展示。系统整体是一个“移动端 + 服务端 + 即时通讯智能体 + 地理服务”的组合式架构。

## 2. 总体架构分层

系统可按访问路径分为五层：

1. 表现层：React Native App、QQ 客户端、运维 QQ 群。
2. 接入层：HFUT REST API、QQ-bot WebSocket 监听、QQ-bot internal HTTP API、NapCat OneBot11 HTTP/WebSocket。
3. 业务层：用户 / 学校 / 文章 / 商品 / 订单 / 通知 / 推荐 / QQ 绑定 / Bot 服务接口。
4. 数据与缓存层：PostgreSQL、Redis、OSS 本地存储、七牛云 Kodo、用户行为表与推荐画像缓存。
5. 外部服务层：Kimi/Moonshot 大模型、NapCat QQ 网关、Martin 矢量瓦片服务、GraphHopper 路线服务、MapLibre GL JS 静态资源服务。

## 3. 关键技术栈

### 3.1 移动端

移动端位于 `hfut-front/HFUTUnion`，采用 React Native 0.82、React 19、TypeScript 5.8。界面导航使用 React Navigation，包括 native stack、bottom tabs 与 material top tabs。状态和本地缓存主要通过组件状态、AsyncStorage、轻量工具模块实现。地图功能通过 `react-native-webview` 加载自包含 MapLibre HTML。

主要依赖包括：

- `@react-navigation/*`：页面导航与 Tab 架构。
- `react-native-webview`：承载 MapLibre 地图。
- `@react-native-async-storage/async-storage`：用户信息、浏览标记等本地缓存。
- `react-native-image-picker` / `react-native-image-crop-picker` / `react-native-image-viewing`：图片选择、裁剪、预览。
- `@notifee/react-native`：本地通知能力。
- `@react-native-community/geolocation`：地图与路线中的定位能力。
- `react-native-vector-icons`：界面图标。

### 3.2 后端

后端位于 `HFUT-Graduation-Project`，使用 Go 1.23、Gin、GORM、PostgreSQL、Redis。主要职责包括用户认证、学校隔离、内容发布、商品订单、通知、评论、收藏点赞、推荐、地图代理、OSS 文件存储、Bot 服务接口。

主要依赖包括：

- Gin：HTTP API 路由与中间件。
- GORM + Postgres driver：ORM 与 PostgreSQL 数据访问。
- `github.com/lib/pq`：PostgreSQL 数组字段等兼容。
- Redis v9：验证码、限流、防抖、推荐画像缓存、seen 集合。
- JWT v5：用户 JWT 与 Bot service-to-service JWT。
- Zap：结构化日志。
- Qiniu SDK：七牛云 Kodo 对象存储。
- Snowflake：分布式 ID，用于图片路径等。
- GoQuery：部分学校信息或外部页面解析能力。

### 3.3 QQ-bot

QQ-bot 位于 `QQ-bot`，使用 Go 1.22。它通过 NapCat 的 OneBot11 WebSocket 接收 QQ 消息，通过 NapCat HTTP API 发送群消息、私聊、图片等，通过 Kimi/Moonshot 进行业务意图识别，并通过 HFUT 后端的 `/api/v1/bot/*` service API 写入结构化数据。

主要依赖包括：

- Gorilla WebSocket：连接 NapCat WebSocket。
- Moonshot SDK：调用 Kimi 模型进行业务识别和聊天。
- Viper：配置读取、环境变量绑定、热重载。
- JWT v5：签发 bot -> hfut 的短时 service token。
- Zap：日志。
- ants：命令执行 worker pool。
- imaging / x/image：图片处理。

## 4. 核心模块说明

### 4.1 移动端模块

移动端按业务页面组织，主要包括：

- 登录与用户信息：登录、用户资料、学校绑定、QQ 认证。
- 社区内容：帖子、求解答、回答详情、评论、点赞、收藏、通知。
- 市集与求物品：二手商品、求物品、订单、订单聊天、请求下架。
- 地图：地址选择、商品位置、订单路线、WebView 地图封装。
- API 层：`src/api/*` 封装后端 REST 调用。
- 工具层：价格格式化、作者名格式化、浏览标记、分页、地图 HTML 生成。

产品语义上目前把帮助类内容分为两类：

- 求解答：对应 `articles.type=2` 的提问，以及 `articles.type=3` 的回答。
- 求物品：对应 `goods.goods_category=2`。如果 `price > 0 && negotiable=false`，前端额外挂“有偿”标签；如果 `price=0 && negotiable=false`，隐藏价格；如果 `negotiable=true`，展示“面议”。

### 4.2 后端业务模块

后端采用 Controller / Service / DAO / Model 分层：

- Controller：处理 HTTP 参数、认证上下文、响应封装，如 `article.go`、`good.go`、`answer_feed.go`、`bot.go`。
- Service：承载业务规则，如文章可见性、商品上下架、订单状态机、QQ 绑定、推荐、通知。
- DAO：封装数据库访问，如 `ArticleStore`、`GoodStore`、`UserStore`、`UserBehaviorStore`。
- Model：对应 PostgreSQL 表结构，如 `Article`、`Good`、`User`、`Order`、`Notification`。

典型接口组包括：

- `/api/v1/user/*`：登录用户资料、学校绑定、QQ 绑定 / 解绑、用户内容列表。
- `/api/v1/post/*`：帖子。
- `/api/v1/question/*`：求解答问题。
- `/api/v1/answer/*`：回答。
- `/api/v1/goods/*`：商品 / 求物品。
- `/api/v1/orders/*`：订单与聊天。
- `/api/v1/comments/*`、`/like/*`、`/collect/*`：互动能力。
- `/api/v1/notifications/*`：站内通知。
- `/api/v1/search/articles`：聚合搜索。
- `/api/v1/config/map`、`/map/tiles/:z/:x/:y`：地图配置与瓦片代理。
- `/api/v1/bot/*`：QQ-bot service account 专用接口。

### 4.3 QQ-bot 模块

QQ-bot 的核心运行单元包括：

- WebSocket 监听：连接 NapCat WebSocket，处理 `message_type=group` 和 `message_type=private`。
- 群消息分流：被 @ 时走命令系统；白名单群的普通消息走自动监听窗口。
- 自动监听窗口：按 `(groupID, userID)` 聚合一段连续消息，沉默窗口后交给 Kimi 识别。
- Kimi 识别：把文本、图片占位符、时间、message_id 结构化成 JSON 输入，要求模型输出 `RecognizeAction`。
- Dispatch：按 action 类型调用 HFUT 后端，如发布商品、求物品、发布求解答、回答、下架、关闭求解答。
- 图片转存：从 NapCat 临时图片 URL 下载图片，再上传到 HFUT OSS，避免 QQ 临时链接过期。
- 内部 HTTP API：供 HFUT 后端反向调用 QQ-bot，如检测好友、发送私聊验证码、发送群消息。
- 运维通知：上架事件、群接入申请等转发到可配置的 ops QQ 群。

当前识别动作包括：

- `publish_good`：二手或求物品，按 `category` 区分。
- `seek_goods`：用户说“收 / 求 / 求购某物”，搜索本校在售二手，同时发布为求物品。
- `publish_question`：发布求解答。
- `publish_answer`：回答群内近期求解答。
- `off_shelf`：下架二手或撤销求物品。
- `close_question`：关闭求解答。
- `none`：不处理。

配额冷却策略已经改为：Kimi quota 冷却或单次 quota error 时直接静默丢弃当前窗口，不使用正则兜底，避免低置信度规则误落库。

## 5. 数据流说明

### 5.1 移动端普通业务流

1. 用户在 React Native App 中发起操作，例如浏览商品、点赞、收藏、发布求解答。
2. API 模块向 HFUT 后端 `/api/v1/*` 发送 JWT 鉴权请求。
3. Gin 中间件解析用户身份、学校 ID 与权限。
4. Controller 调用 Service，Service 调用 DAO 读写 PostgreSQL。
5. 对互动行为，后端写入点赞、收藏、评论、通知表，并调用推荐服务记录行为。
6. Redis 用于推荐画像缓存、seen 集合、验证码、限流和浏览量防抖。
7. 响应经 Controller enrich 后返回前端，前端更新页面状态。

### 5.2 QQ 群自动上架 / 求物品流

1. 群用户在白名单 QQ 群里发送自然语言消息，例如“出鞋架 6 元”或“收笔记本支架”。
2. NapCat 通过 OneBot11 WebSocket 推送群消息给 QQ-bot。
3. QQ-bot 按 `(group, user)` 聚合窗口，沉默后调用 Kimi 识别。
4. Kimi 返回结构化 action。
5. QQ-bot 先通过 `/api/v1/bot/users/qq-child` upsert QQ 旗下账号。
6. 发布类 action 调用 `/api/v1/bot/goods` 或 `/api/v1/bot/articles`，图片先经 `/api/v1/bot/images` 转存。
7. HFUT 后端按 QQ 群所属学校写入数据，并返回结果。
8. QQ-bot 在群里以简短文案反馈，同时把上架事件转发到 ops 群。

### 5.3 QQ 绑定 / 解绑流

1. 用户在 App 的 QQ 认证页输入 QQ 号。
2. HFUT 后端校验用户已绑定学校，并调用 QQ-bot internal API `/internal/qq/check-friend`。
3. QQ-bot 通过 NapCat `get_friend_list` 或相关接口判断是否可私聊。
4. HFUT 后端生成验证码并写入 Redis，调用 `/internal/qq/send-private` 让 QQ-bot 私聊验证码。
5. 用户在 App 输入验证码，HFUT 后端校验 Redis 中的验证码。
6. 绑定成功后，主账号与 QQ 旗下账号建立关联；解绑则临时丢失 QQ 关联数据，重新绑定可恢复关联。

### 5.4 孤儿 QQ 商品请求下架流

1. App 用户看到 QQ 群上架但发布者未绑定 App 的商品。
2. 前端展示 QQ 联系方式和“请求下架”按钮，不进入 App 订单聊天。
3. 用户点击后，HFUT 后端按商品 `created_in_group_id` 或用户 `created_in_group_id` 找到 QQ 群。
4. 后端反向调用 QQ-bot `/internal/qq/send-group`，在 QQ 群 @ 发布者确认。
5. 二手商品询问“是否已经出”；求物品询问“是否已经求得该物品”。
6. 发布者回复“是 / 已出 / 已找到 / 求到了”等，QQ-bot 识别为 `off_shelf` 并调用后端下架。

### 5.5 浏览量防抖流

帖子、求解答、回答和商品详情页都会触发浏览计数。由于点赞 / 收藏后前端会再次拉详情刷新计数，后端通过 Redis 做 300 秒防抖：

1. Service Get 方法通过 `ShouldCountView(userID, extType, extID)` 判断是否计数。
2. Redis `SETNX view:debounce:{extType}:{extID}:{userID}`，TTL 为 300 秒。
3. 如果首次写入成功，则 PostgreSQL 中 `view_count = view_count + 1`。
4. 如果 300 秒内重复进入、点赞后刷新或收藏后刷新，则不再增加浏览。
5. Redis 不可用时保守不计数，不影响业务请求。

## 6. 数据存储与缓存设计

### 6.1 PostgreSQL

PostgreSQL 是主数据库，保存用户、学校、帖子、求解答、回答、商品、订单、订单消息、评论、点赞、收藏、通知、用户行为等。典型表包括：

- `users`：用户、QQ 旗下账号、学校绑定信息。
- `articles`：帖子、求解答、回答统一表，`type=1/2/3` 区分。
- `goods`：二手商品与求物品统一表，`goods_category=1/2` 区分。
- `orders`、`order_messages`：商品 / 求物品交易与聊天。
- `comments`：评论与回复。
- `likes`、`collect`、`collect_items`：点赞与收藏。
- `notifications`：站内通知。
- `user_behaviors`：推荐系统行为日志。
- `tags`：标签。

文章和商品均维护 `like_count`、`collect_count`、`view_count` 等冗余计数字段，用于列表展示和推荐排序。

### 6.2 Redis

Redis 用于高频、短时、可丢失或可重算的数据：

- QQ 绑定验证码与解绑验证码。
- QQ 绑定错误次数、锁定和发送限流。
- 订单消息加急等限流。
- 推荐画像缓存。
- 推荐流 seen 集合。
- 浏览量 300 秒防抖 key。

### 6.3 OSS / 七牛云 Kodo

文件存储支持本地 OSS 与七牛云 Kodo。后端通过 `OSSDriver` 配置决定新上传文件写本地还是七牛。前端与 QQ-bot 上传的图片都会通过后端统一存储，返回可访问 URL。QQ-bot 会把 NapCat 临时图片先下载再转存到 HFUT OSS，保证商品图长期有效。

## 7. 地图与瓦片架构

地图体系由移动端 WebView、后端地图配置 / 代理接口，以及上一级目录的地图服务仓库 `map-project` 共同组成。`map-project` 记录了瓦片生成、Martin 服务、GraphHopper 路线服务的部署过程，是论文中“地理信息服务搭建”章节的重要依据。

### 7.1 端侧地图渲染

React Native 端通过 `MapWebView` 加载自包含 HTML。HTML 内使用 MapLibre GL JS 渲染矢量地图，React Native 与 WebView 之间通过 `window.ReactNativeWebView.postMessage` 和注入 JavaScript 指令通信。

地图支持三种模式：

- picker：地图中心准星选点，React Native 读取中心经纬度。
- route：给定起点和终点，调用 GraphHopper 画线路；失败时降级为直线距离。
- view：只展示 marker。

### 7.2 地图服务仓库 `map-project`

地图服务搭建过程位于 `/Users/dp/Documents/go-proj/private/map-project`。该仓库体现了地图底座从 OSM 数据到可访问 Web 服务的完整链路：

1. 原始数据：安徽区域 PBF 文件 `/personal/anhui-260221.osm.pbf`。
2. 瓦片生成：使用 Planetiler 将 PBF 生成 `tiles.mbtiles`，schema 为 OpenMapTiles。
3. 瓦片发布：使用 Martin tile server 读取 `tiles.mbtiles`，对外提供 TileJSON、MVT 瓦片、WebUI 与 metrics。
4. 路线服务：使用 GraphHopper 导入同一 PBF，生成 `gh-graph` 路网索引。
5. 前端集成：移动端 MapLibre 读取 Martin 的矢量瓦片，路线模式调用 GraphHopper `/route`。

部署脚本 `deploy-ubuntu.sh` 体现了无 Docker、无 systemd 的 Ubuntu 部署方式：

- 安装 OpenJDK 21、curl、wget、screen。
- 下载 Planetiler。
- 预下载自然地理、水系等 OpenMapTiles 依赖数据源。
- 执行 Planetiler 生成 `~/map-server/data/tiles.mbtiles`。
- 下载 Martin，并用 screen 启动。
- Martin 默认监听 `50001`，GraphHopper 默认监听 `50002`。

### 7.3 Martin 瓦片服务

Martin 服务基于 `tiles.mbtiles`（安徽省 OpenMapTiles 矢量瓦片）部署。`MARTIN-API-WEBUI.md` 记录了主要端点：

- `GET /`：Martin WebUI。
- `GET /catalog`：数据源目录。
- `GET /tiles`：TileJSON 元数据。
- `GET /tiles/{z}/{x}/{y}`：MVT 矢量瓦片。
- `GET /health`：健康检查。
- `GET /_/metrics`：Prometheus 指标。

当前前端配置使用 `MAP_TILE_BASE=http://oeiw1426422.bohrium.tech:50001`，瓦片 URL 为 `/tiles/{z}/{x}/{y}`，最大 zoom 为 14。

### 7.4 GraphHopper 路线服务

GraphHopper 部署配置位于 `map-project/config-gh.yml`，检查清单位于 `GRAPHHOPPER-CHECKLIST.md`。当前配置包括：

- profiles：`car`、`foot`、`bike`、`racingbike`、`mtb`。
- profiles_ch：上述 5 个 profile 均启用 CH 加速。
- encoded values：包含 car、foot、bike、racingbike、mtb 的 access、average_speed、priority、road_access 等。
- server：`0.0.0.0:50002`。

移动端默认使用 `foot` profile。路线请求示例：

```text
GET /route?point=31.8,117.2&point=31.9,117.3&profile=foot&points_encoded=false
```

### 7.5 当前地图配置中出现的服务

- Martin 瓦片服务：`MAP_TILE_BASE`，OpenMapTiles schema，zoom 0-14，覆盖安徽省。
- GraphHopper 寻路服务：`MAP_ROUTE_BASE`。
- MapLibre 静态资源服务：`MAP_STATIC_BASE`。

## 8. 推荐与搜索

后端推荐采用“行为画像 + 双路召回 + 打散”的方案：

- 行为来源：浏览、点赞、收藏、评论、搜索。
- 行为权重：view、like、collect、comment、search 均可配置。
- 画像维度：标签、作者、行为时间衰减。
- Redis 缓存用户画像，减少每次请求聚合成本。
- seen 集合记录短期已曝光 / 已浏览内容，用于推荐去重。
- 文章与商品都接入推荐；商品推荐目前将 collect、like、view 共同作为热度组成。

搜索方面：

- 文章聚合搜索覆盖帖子、求解答、回答。
- PostgreSQL 侧有相关度、热度和 combined 排序。
- 热度公式使用收藏、点赞、浏览，并带时间衰减。

## 9. 安全与鉴权

系统包含三类鉴权：

1. 用户 JWT：App 调 HFUT 后端 API 时使用，后端中间件解析用户 ID、学校 ID、角色。
2. Bot service JWT：QQ-bot 调 HFUT `/api/v1/bot/*` 时使用 `X-Bot-Service-Token`，HS256，60 秒有效期，issuer 为 `HFUT-Graduation-Project-bot`。
3. HFUT -> Bot internal JWT：后端反向调用 QQ-bot internal API 时使用 `X-Service-Token`，issuer 为 `HFUT-Graduation-Project-hfut`。

两个 service JWT 共用一个 secret，但通过 issuer 区分方向，避免 token 被错误复用。QQ-bot internal API 设计为只在内网可达，不建议暴露到公网。

## 10. 运行时进程视角

系统运行时至少包括以下进程或服务：

- React Native App：运行在 Android / iOS 设备。
- HFUT API Server：Go Gin HTTP 服务。
- QQ-bot：Go 进程，包含 WebSocket 监听、auto reply scanner、internal HTTP server。
- NapCat：QQ 网关，连接真实 QQ 账号，对外提供 OneBot11 HTTP / WebSocket。
- PostgreSQL：主数据存储。
- Redis：缓存、限流、验证码、防抖、推荐。
- OSS / Qiniu：图片与文件存储。
- Martin：矢量瓦片服务。
- GraphHopper：路线规划服务。
- MapLibre 静态服务：托管 MapLibre JS/CSS。
- Kimi/Moonshot：自然语言识别和聊天模型。

## 11. 关键设计取舍

### 11.1 为什么 QQ-bot 不用正则兜底语义识别

项目早期曾考虑在大模型 quota 冷却时用正则兜底识别，但后续取消。原因是正则无法可靠理解语义，例如“收纳箱 80 元”是卖东西而“收箱子”是求物品，“不要了”是否表示撤销要看最近上下文。误落库的代价比漏识别更高。因此目前策略是：Kimi 可用时识别；Kimi quota 冷却或单次 quota 错时静默。

### 11.2 为什么浏览量要用 Redis 防抖

点赞、收藏后前端会重新拉详情以刷新计数，这会自然触发 GET 详情。如果每次 GET 都增加浏览量，会把互动操作误计为新浏览。因此后端以 `(userID, extType, extID)` 为 key 做 300 秒 Redis 防抖。这样保留真实进入详情的浏览，同时压住刷新、重试、点赞收藏后的重复请求。

### 11.3 为什么 QQ 群上架使用“旗下账号”

QQ 群用户不一定注册 App。为了让 QQ 群自然语言发布的内容可以落库，系统为 QQ 号创建“QQ 旗下账号”。当用户在 App 中完成 QQ 认证后，主账号可以继承该 QQ 旗下账号发布的内容；解绑后会暂时丢失 QQ 关联数据，重新绑定后恢复。

### 11.4 为什么地图使用 WebView + MapLibre

React Native 原生地图 SDK 往往依赖第三方地图平台，而本项目需要自建瓦片服务和校内选点能力。使用 WebView 运行 MapLibre 可以复用 Web 地图生态，直接加载 Martin 矢量瓦片，并通过 postMessage 与 React Native 交互，降低端侧原生 SDK 集成复杂度。

## 12. 当前可作为论文图示的核心流程

建议论文中至少绘制三张流程图：

1. 总体系统架构图：App、QQ、Bot、HFUT 后端、PostgreSQL、Redis、OSS、地图服务、Kimi。
2. QQ 群自动上架流程图：群消息 -> NapCat -> QQ-bot -> Kimi -> HFUT Bot API -> PostgreSQL / OSS -> 群反馈。
3. App 详情浏览与互动流程图：App -> HFUT API -> Redis 防抖 -> PostgreSQL view_count -> user_behavior -> 推荐画像。

本次同时创建的 Canvas 文件可作为第一张“总体系统架构图”的可视化初稿。
