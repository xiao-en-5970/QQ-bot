// Package kimi 的 ops_sql.go 提供"运维群提问 → 只读 SQL"的 LLM 助手。
//
// 跟 RecognizeBusinessActions 一样不走 QAS / 不走 tools，只是单轮 system+user 调
// completions 强制 JSON 输出。两次调用：
//
//  1. GenerateOpsSQL：给 schema 摘要 + 用户中文问题，输出 { "sql": "..." }
//  2. SummarizeOpsResult：给原问题 + 真实结果（前 N 行 JSON），输出中文总结
//
// 设计动机（为什么不复用 Chat）：
//   - 运维查询是无状态请求，不应污染聊天上下文
//   - 不希望 LLM 在 ops 路径调任意 tool（read_skill / search_jm 等都没意义）
//   - 用 ResponseFormat=JSONObject 强约束输出结构，比解析自由文本稳

package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"qq_bot/conf"

	"github.com/northes/go-moonshot"
)

// opsSQLSchemaSummary 喂给 LLM 的"重要表 + 列摘要"。
//
// 不放完整 schema（500+ 列会爆 token 也分散注意力）；只列运维问得到的核心表 +
// 关键字段 + 一行注释。LLM 想知道更多细节可以让它**先**自己写 SQL 探索
// information_schema.columns，但实测大多数运维问题这份摘要够用。
//
// 维护：每次新增/重命名核心字段时同步更新这里。这部分内容跟生产数据 schema 强耦合，
// 出错代价是 LLM 写出语法对但语义错的 SQL；运维一眼能看出来，所以容忍偏差。
//
// 关键陷阱（之前踩过的，**不要让 LLM 再踩**）：
//   - bot_dispatch_event.action 是"派发后的业务动作"（publish_good / off_shelf 等），
//     不存在叫 'recognize' 的值；每行本身就是一次语义识别+派发，问"语义判断"应直接
//     查全表行而不是按 action='recognize' 过滤。
//   - status / good_status / goods_category / role / ext_type / type 等都是 smallint
//     枚举，**写 SQL 时用整型字面量**（status=1）不要写字符串（status='valid'）。
const opsSQLSchemaSummary = `<重要 schema 摘要 / PostgreSQL>

# 业务表

users(id, username, school_id, account_type[1普通 2QQ旗下], parent_user_id, qq_number,
      created_in_group_id, status[smallint: 1正常 2禁用],
      role[smallint: 1普通 2管理员 3超管 4匿名], created_at, updated_at)

schools(id, name, code, status, created_at)

goods(id, user_id, school_id, title, content,
      goods_category[smallint: 1二手 2求物品],
      good_status[smallint: 1在售 2下架 3售出],
      status[smallint: 1正常 2删除], negotiable, bargain, price[整数, 单位:分],
      view_count, collect_count, like_count, created_at, updated_at)

articles(id, user_id, school_id, type[smallint: 1帖子 2提问 3回答],
         parent_id, title, content,
         status[smallint: 1正常 2删除 3草稿],
         view_count, collect_count, like_count, created_at, updated_at)

orders(id, good_id, buyer_user_id, seller_user_id, status, total_price, created_at)

user_behaviors(id, user_id,
              ext_type[smallint: 1帖子 2提问 3回答 4商品], ext_id,
              action[smallint: 1view 2like 3unlike 4collect 5uncollect 6comment 7search],
              weight, keyword, created_at)

# 运维 / 指标表（系统自带，bot 自动写入）

metric_minute(minute_ts[bigint, 对齐到分钟的 epoch 秒], source, metric, value[bigint])
  source = 'http'：HTTP 后端
    metric ∈ {requests, errors_4xx, errors_5xx, biz_errors, latency_sum, latency_count}
  source = 'bot'：QQ-bot
    metric ∈ {ws_msgs, ws_group_msgs, ws_private_msgs,
              recognize_called, recognize_success, recognize_fail, quota_cooling,
              dispatch_success, dispatch_fail, dispatch_other,
              rate_limit, private_access, ops_notify}
  时间窗：用 EXTRACT(EPOCH FROM now() - INTERVAL '1 hour')::bigint 跟 minute_ts 比较
  累计：SUM(value) GROUP BY metric

bot_dispatch_event(id, occurred_at[timestamptz], group_id, user_id, action[varchar],
                   outcome[varchar], title, category, price_cents, confidence[real],
                   reason[text], err_message[text])
  ★ 每一行 = QQ-bot 完成的一次"语义识别 + 业务派发"事件。
    "查 bot 的语义判断 / 识别记录"= 直接 SELECT 该表，不需要按 action 过滤。
  action 取值（**全部都已经是识别完成后的派发动作**，不存在 'recognize' 这个值）：
    publish_good / seek_goods / off_shelf / publish_question / publish_answer
    chitchat / unknown / ...
  outcome 取值：ok / err / rate_limited / success / fail / dup / ignore / ask_user
  reason  Kimi 给出的中文判断依据（"用户先发图片再说出+物品+价格"等）
  confidence  Kimi 给的置信度 [0,1]
  category 仅 publish_good / seek_goods 时填：1=二手 2=求物品 3=求解答

</重要 schema 摘要>`

// opsSQLSystemPrompt 给 LLM 的系统提示词——讲清楚约束。
//
// 关键约束：
//   - 必须 SELECT 开头（也允许 WITH/EXPLAIN/SHOW），不要 UPDATE/INSERT/DELETE/DDL
//   - 不要分号（hfut 那边的 validateReadOnlySQL 会拒绝多语句）
//   - 不要 LIMIT（hfut 自动套 LIMIT 200）；除非用户明确要求 top N
//   - 时间相关用 PG 函数：now() / NOW() - INTERVAL '1 hour' / EXTRACT(EPOCH FROM ...)
//   - 输出 JSON：{"sql": "..."}；不要任何其它字段或解释，不要 markdown 围栏
const opsSQLSystemPrompt = `你是一名"只读 SQL 助手"，根据运维同事的中文问题，生成一段 PostgreSQL SELECT 查询。

约束（违反约束会被静态检查拒绝执行）：
1. 必须以 SELECT 或 WITH 开头；禁止 UPDATE / INSERT / DELETE / CREATE / ALTER / DROP / TRUNCATE / GRANT / VACUUM / COPY
2. 整段 SQL 中不允许出现分号
3. 不要自行 LIMIT，后端会自动套 LIMIT 200；除非用户明确说要 top N
4. 时间表达用 PG 内置：now() / NOW() - INTERVAL 'X minutes/hours/days'
5. 涉及 metric_minute 时用 EXTRACT(EPOCH FROM now())::bigint 跟 minute_ts 比较
6. 字段含义请严格对照下方 schema；不要臆造列名 / 不要臆造枚举值

` + opsSQLSchemaSummary + `

# 常见问法 → 对应 SQL

| 中文问题 | 应当写 |
| --- | --- |
| "近 N 小时 bot 的语义判断 / 识别记录 / 派发事件" | SELECT occurred_at, group_id, user_id, action, outcome, title, category, confidence, reason FROM bot_dispatch_event WHERE occurred_at > NOW() - INTERVAL 'N hours' ORDER BY occurred_at DESC |
| "近 N 小时 bot 派发失败 / 识别失败 / 异常" | 同上但 WHERE outcome IN ('err','fail','rate_limited') AND occurred_at > NOW() - INTERVAL 'N hours' |
| "近 N 小时接口请求量 / 错误率" | WITH win AS (SELECT metric, SUM(value) v FROM metric_minute WHERE source='http' AND minute_ts >= EXTRACT(EPOCH FROM NOW() - INTERVAL 'N hours')::bigint GROUP BY metric) SELECT * FROM win |
| "在售商品 / 提问数" | SELECT count(*) FROM goods WHERE status=1 AND good_status=1 [AND school_id=X] |
| "活跃用户" | SELECT count(DISTINCT user_id) FROM user_behaviors WHERE created_at > NOW() - INTERVAL 'N days' |

特别提醒：
- bot_dispatch_event.action 不存在 'recognize' / 'recognise' 值——每一行已经是识别后的事件。
- enum 字段一律用整型字面量（status=1 不要写 status='valid'）。
- timestamptz 字段直接跟 NOW() 比较；epoch 字段用 EXTRACT(EPOCH FROM ...)::bigint。

输出严格 JSON（无 markdown 围栏、无解释）：{"sql": "<纯 SELECT 语句>"}`

// buildOpsSQLMessages 按 cache 命中与否构造 ops_sql 调用的 messages 序列。
func buildOpsSQLMessages(cacheID, question string) []*moonshot.ChatCompletionsMessage {
	userContent := "运维问题：" + question
	if cacheID != "" {
		return []*moonshot.ChatCompletionsMessage{
			cacheReferenceMessage(cacheID),
			{Role: moonshot.RoleUser, Content: userContent},
		}
	}
	return []*moonshot.ChatCompletionsMessage{
		{Role: moonshot.RoleSystem, Content: opsSQLSystemPrompt},
		{Role: moonshot.RoleUser, Content: userContent},
	}
}

// opsSummarySystemPrompt 第二轮总结时的提示词——把结果换成中文一段话。
const opsSummarySystemPrompt = `你是运维助手，刚刚执行了一段只读 SQL，结果以 JSON 的形式提供给你。
请把结果用一两句中文概括给运维同事看，必要时点出关键数字 / 趋势 / 异常。
输出纯文本，不要 JSON 不要 markdown，不要复述 SQL，不要超过 300 字。`

// GenerateOpsSQL 用 Kimi 生成一段只读 SQL；返回 SQL 字符串。
//
// 失败：
//
//   - ErrQuotaCooling：quota gate 冷却中，调用方应当回复"运维助手暂时过载稍后再试"
//   - 其它 error：网络 / API / 解析失败
func (k *Kimi) GenerateOpsSQL(ctx context.Context, question string) (string, error) {
	if k == nil {
		return "", errors.New("kimi 未启用")
	}
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		return "", ErrQuotaCooling
	}
	model := moonshot.ChatCompletionsModelID(conf.Cfg.Gpt.RecognizeModel)
	// 优先用 Context Cache 省 system prompt token；失效 / 创建失败时 fallback 老路
	cacheID := k.opsSQLCache.loadID()
	req := &moonshot.ChatCompletionsRequest{
		Model:       model,
		Messages:    buildOpsSQLMessages(cacheID, question),
		Temperature: 0.2,
		ResponseFormat: &moonshot.ChatCompletionsRequestResponseFormat{
			Type: moonshot.ChatCompletionsResponseFormatJSONObject,
		},
	}
	resp, err := k.cli.Chat().Completions(ctx, req)
	if err != nil && cacheID != "" && k.opsSQLCache.dropIfMissing(err, k.cli) {
		req.Messages = buildOpsSQLMessages("", question)
		resp, err = k.cli.Chat().Completions(ctx, req)
	}
	globalQuotaGate.RecordResult(err)
	if err != nil {
		return "", fmt.Errorf("调用 moonshot completions 失败: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("moonshot 返回 0 choices")
	}
	raw := resp.Choices[0].Message.Content
	var parsed struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", fmt.Errorf("解析 sql JSON 失败 raw=%q: %w", truncateLog(raw, 400), err)
	}
	if parsed.SQL == "" {
		return "", errors.New("LLM 返回了空 sql")
	}
	return parsed.SQL, nil
}

// SummarizeOpsResult 把 SQL 执行结果（columns + rows）交给 LLM 总结成一段中文。
//
// 失败时（含 quota cooling / 网络错）调用方可以选择直接拼一份 ASCII 表回复，
// 不强求总结成功。
func (k *Kimi) SummarizeOpsResult(ctx context.Context, question, sqlStr string, columns []string, rows [][]any) (string, error) {
	if k == nil {
		return "", errors.New("kimi 未启用")
	}
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		return "", ErrQuotaCooling
	}
	// 只把前 30 行喂给 LLM——更多没意义、还会爆 token
	snippet := rows
	if len(snippet) > 30 {
		snippet = snippet[:30]
	}
	payload, _ := json.Marshal(map[string]any{
		"question":   question,
		"sql":        sqlStr,
		"columns":    columns,
		"rows":       snippet,
		"row_count":  len(rows),
		"truncated":  len(rows) > len(snippet),
	})
	model := moonshot.ChatCompletionsModelID(conf.Cfg.Gpt.RecognizeModel)
	resp, err := k.cli.Chat().Completions(ctx, &moonshot.ChatCompletionsRequest{
		Model: model,
		Messages: []*moonshot.ChatCompletionsMessage{
			{Role: moonshot.RoleSystem, Content: opsSummarySystemPrompt},
			{Role: moonshot.RoleUser, Content: "结果上下文（JSON）：" + string(payload)},
		},
		Temperature: 0.4,
	})
	globalQuotaGate.RecordResult(err)
	if err != nil {
		return "", fmt.Errorf("调用 moonshot completions 失败: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("moonshot 返回 0 choices")
	}
	return resp.Choices[0].Message.Content, nil
}
