# 运维群只读 SQL 查询

> **文件名以 `_` 开头是有意的**——`utils/kimi/tools_skill.go::isSkillPathAllowed`
> 拒绝读取任何路径段以 `_` 开头的 skill 文件，避免 Kimi 在普通群 / 私聊里读到这份
> 文档后向无权访问的用户暴露"运维群里 @bot 能跑 SQL"这件事的存在。这份文档**只
> 给开发者 / 运维同事看**，不是 LLM 上下文的一部分。维护时不要改名去掉下划线。

bot 在运维群（`conf.Bot.OpsGroupIDs`，可多个）支持"自然语言提问 → 只读 SQL"。

## 触发方式

在任意 `OpsGroupIDs` 群里 **@bot 后写中文问题**：

```
@bot 近 1 小时接口请求量
@bot 看一下 metric_minute 里 errors_5xx 的趋势，最近 6 小时按分钟列出
@bot 二手在售商品有多少，按学校分组
@bot 最近 20 条 bot 派发失败的事件，把 reason 列出来
```

bot 收到后：

1. 把问题 + schema 摘要喂给 Kimi → 输出 `{"sql": "SELECT ..."}`
2. 调 `POST /api/v1/bot/admin/sql` 在 hfut 后端执行
3. 把结果再喂给 Kimi 一句话总结
4. 群里发回：`总结 + SQL + 行数耗时 + 前 5 行 ASCII 表`

## 安全约束

四道独立防线，互为冗余：

1. **路由鉴权**：`/api/v1/bot/admin/sql` 在 BotServiceAuth 路由组下，只有持共享 `HFUT_API_JWT_SECRET` 自签 service JWT 的请求能进——QQ 用户和外部都打不到。
2. **静态白名单**：SQL 必须以 `select` / `with` / `explain` / `show` / `values` 开头；任何 `update / insert / delete / create / alter / drop / truncate / grant / vacuum / copy` 直接 400。SQL 中**不允许出现分号**（拒绝多语句）。
3. **PG 事务级 READ ONLY**：执行时 `BEGIN READ ONLY` + `SET LOCAL statement_timeout = '10s'`；即便绕过静态检查，PG 自己也会拒绝写入并强制超时。
4. **行数限流**：SELECT/WITH 自动包一层 `SELECT * FROM (...) LIMIT 200`（最大 1000）；EXPLAIN/SHOW/VALUES 按原样执行（这些天然不会有海量行）。

## 配置

bot `.env`：

```ini
# 多运维群，逗号分隔。任一群里 @bot 都能用 ops_query。
BOT_OPS_GROUP_IDS=1084352497,1234567890

# 老的单值 BOT_OPS_GROUP_ID 也仍然支持（仅当 IDS 为空时回退）
# BOT_OPS_GROUP_ID=1084352497

# Kimi 必须配（GPT_API_KEY），否则运维助手会回复"未启用"
GPT_API_KEY=...

# bot 调 hfut 也必须配（共享 secret）
HFUT_API_URL=https://api.xiaoen.xyz
HFUT_API_JWT_SECRET=<和 hfut 那边的 BOT_SERVICE_JWT_SECRET 同一个值>
```

hfut `.env`：

```ini
# 必须 = bot 那边的 HFUT_API_JWT_SECRET（一对镜像）
BOT_SERVICE_JWT_SECRET=<32 位以上 hex / base64>
```

## Schema 摘要

LLM 拿到的 schema 上下文写在 `utils/kimi/ops_sql.go::opsSQLSchemaSummary`。
重要表：

| 表 | 用途 |
|---|---|
| `users / schools / goods / articles / orders / user_behaviors` | 业务核心 |
| `metric_minute(minute_ts, source, metric, value)` | HTTP + bot 时序持久化 |
| `bot_dispatch_event(occurred_at, action, outcome, reason, ...)` | bot 自动识别 + 派发事件流 |

新加表 / 改字段时记得同步更新 `opsSQLSchemaSummary`，否则 LLM 写的 SQL 会踩字段名。

## 失败兜底

| 场景 | 群里看到的反馈 |
|---|---|
| Kimi quota 冷却 | "运维助手过载（Kimi 冷却中），请稍后再试" |
| LLM 给的 SQL 不合法 | "SQL 执行失败：仅允许 select/with/... 收到首词 \"update\""（带 SQL 全文） |
| PG 拒绝（DML / 超时） | hfut 报错文案 + SQL（运维肉眼看） |
| Kimi 总结失败 | 退化为机械摘要 "查询执行成功，共返回 N 行结果"，仍然带 SQL + 表前 5 行 |
| GPT_API_KEY 未配 | "运维助手未启用：GPT_API_KEY 未配置" |
| HFUT_API_URL 未配 | "运维助手未启用：HFUT_API_URL / HFUT_API_JWT_SECRET 未配置" |

## 运维示例提问

| 问题 | 期望生成的 SQL |
|---|---|
| 近 1 小时接口请求量 | `SELECT SUM(value) FROM metric_minute WHERE source='http' AND metric='requests' AND minute_ts >= EXTRACT(EPOCH FROM NOW() - INTERVAL '1 hour')::bigint` |
| 近 1 小时接口正确率 | `WITH win AS (SELECT metric, SUM(value) v FROM metric_minute WHERE source='http' AND minute_ts >= EXTRACT(EPOCH FROM NOW() - INTERVAL '1 hour')::bigint GROUP BY metric) SELECT (SELECT v FROM win WHERE metric='requests') - COALESCE((SELECT v FROM win WHERE metric='errors_5xx'),0) - COALESCE((SELECT v FROM win WHERE metric='errors_4xx'),0) AS ok, (SELECT v FROM win WHERE metric='requests') AS total` |
| 学校 1 在售商品数 | `SELECT goods_category, COUNT(*) FROM goods WHERE school_id=1 AND status=1 AND good_status=1 GROUP BY goods_category` |
| 最近 10 条 bot 派发失败 | `SELECT occurred_at, group_id, action, reason, err_message FROM bot_dispatch_event WHERE outcome='err' ORDER BY occurred_at DESC` |

## 限制 / 已知不支持

- **写入永远不行**——这是设计，不是 bug
- 单语句执行（不允许分号）——复杂查询请用 `WITH` CTE
- 单次查询硬超时 10s
- 单 cell 超 4KB 截断（防 BLOB / 长 jsonb 拖慢）
- LLM 偶尔会写错字段名——容忍：返回的 PG 错误会原样回到群里，运维改下重发即可
