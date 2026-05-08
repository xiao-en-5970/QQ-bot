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
const opsSQLSchemaSummary = `<重要 schema 摘要 / Postgres>
users(id, username, school_id, account_type, parent_user_id, qq_number,
      created_in_group_id, status[1正常 2禁用], role[1普通 2管理员 3超管 4匿名], created_at)
schools(id, name, code, status, created_at)
goods(id, user_id, school_id, title, content, goods_category[1二手 2求物品],
      good_status[1在售 2下架 3售出], status, negotiable, bargain, price[分],
      view_count, collect_count, like_count, created_at, updated_at)
articles(id, user_id, school_id, type[1帖子 2提问 3回答], parent_id, title, content,
         status[1正常 2删除 3草稿], view_count, collect_count, like_count, created_at)
orders(id, good_id, buyer_user_id, seller_user_id, status, total_price, created_at)
user_behaviors(id, user_id, ext_type[1帖子 2提问 3回答 4商品], ext_id,
              action[1view 2like 3unlike 4collect 5uncollect 6comment 7search],
              weight, keyword, created_at)

# 运维持久化表（自带）
metric_minute(minute_ts, source[http|bot], metric, value)
  source=http 时 metric ∈ {requests, errors_4xx, errors_5xx, biz_errors,
                            latency_sum, latency_count}
  source=bot  时 metric ∈ {ws_msgs, ws_group_msgs, ws_private_msgs,
                            recognize_called, recognize_success, recognize_fail,
                            quota_cooling, dispatch_success, dispatch_fail,
                            dispatch_other, rate_limit, private_access, ops_notify}
  minute_ts 是 epoch 秒，对齐到分钟。EXTRACT(EPOCH FROM now())::bigint 取当前秒。

bot_dispatch_event(id, occurred_at[timestamptz], group_id, user_id, action,
                   outcome[ok|err|rate_limited], title, category, price_cents,
                   confidence, reason, err_message)
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
6. 字段含义请严格对照下方 schema；不要臆造列名

` + opsSQLSchemaSummary + `

输出严格 JSON（无 markdown 围栏、无解释）：{"sql": "<纯 SELECT 语句>"}`

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
	resp, err := k.cli.Chat().Completions(ctx, &moonshot.ChatCompletionsRequest{
		Model: model,
		Messages: []*moonshot.ChatCompletionsMessage{
			{Role: moonshot.RoleSystem, Content: opsSQLSystemPrompt},
			{Role: moonshot.RoleUser, Content: "运维问题：" + question},
		},
		Temperature: 0.2,
		ResponseFormat: &moonshot.ChatCompletionsRequestResponseFormat{
			Type: moonshot.ChatCompletionsResponseFormatJSONObject,
		},
	})
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
