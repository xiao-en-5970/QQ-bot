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
	"time"

	"qq_bot/conf"
	zaplog "qq_bot/utils/zap"

	openai "github.com/sashabaranov/go-openai"
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
//
// 实时 schema 注入：从 liveSchemaCache 拿一份"information_schema 快照"作为**单独的
// user 消息**前置——而不是拼进 system prompt。这样 system prompt 哈希不变，Context
// Cache 持续命中（每次省几千 token 的 schema 摘要）；实时 schema 每次随 user 消息
// 重新发，不影响 cache 但能让 LLM 看到生产库的最新列名。
//
// 实时 schema 为空（启动后首拉未完 / 一直失败）→ 退化到纯硬编码 schema（system prompt 内）。
func buildOpsSQLMessages(cacheID, question string) []openai.ChatCompletionMessage {
	msgs := make([]openai.ChatCompletionMessage, 0, 4)
	if cacheID != "" {
		msgs = append(msgs, cacheReferenceMessage(cacheID))
	} else {
		msgs = append(msgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: opsSQLSystemPrompt})
	}
	// 注入实时 schema（仅命名/类型，业务语义注释仍在 system prompt 的硬编码 schema 里）。
	// 30000 字硬上限——绝大多数 schema 远小于此，超出时截断防止 user 消息过大。
	if live := loadLiveSchema(); live != "" {
		if len(live) > 30000 {
			live = live[:30000] + "\n...(truncated)"
		}
		msgs = append(msgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: "以下是来自 information_schema 的最新表/列/类型快照（**与上方硬编码 schema 摘要互补**——业务语义参考上方注释，最新列名/类型以本快照为准；若两者冲突以本快照为准）：\n\n" + live,
		})
	}
	msgs = append(msgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "运维问题：" + question})
	return msgs
}

// opsSummarySystemPrompt 第二轮总结时的提示词——把结果换成中文一段话。
const opsSummarySystemPrompt = `你是运维助手，刚刚执行了一段只读 SQL，结果以 JSON 的形式提供给你。
请把结果用一两句中文概括给运维同事看，必要时点出关键数字 / 趋势 / 异常。
输出纯文本，不要 JSON 不要 markdown，不要复述 SQL，不要超过 300 字。`

// GenerateOpsSQL 用 Kimi 生成一段只读 SQL；返回 SQL 字符串。
//
// 模型选择：
//
//	主模型 = conf.Cfg.Gpt.RecognizeModel（默认 kimi-k2-0905-preview）
//	  K2 是 preview 模型 + thinking 推理，对"SQL 生成"这种重推理任务
//	  在过载窗口里**稳定踩坑** engine_overloaded（即便重试也大概率失败）；
//	  识别那种"打标签"类轻推理任务在 K2 上不踩这个坑。
//
//	降级模型 = conf.Cfg.Gpt.OpsFallbackModel（默认 moonshot-v1-auto）
//	  v1 系列是稳定版、推理短、配额松。当 K2 持续过载时自动切到 v1
//	  继续出 SQL——对"SQL 生成"这种结构化任务 v1 也完全够用。
//
// 重试链路（4 次机会）：
//
//	① 主模型 → 失败但 retryable
//	② 主模型重试（短退避 1.5s）→ 还失败且仍 retryable
//	③ 降级到 fallback 模型重试（再退避 1s） → 还失败
//	④ 返回错误（上层 ops_query.go 给运维群发"已自动重试 + 降级仍失败"的人话提示）
//
// 失败：
//
//   - ErrQuotaCooling：quota gate 冷却中，调用方应当回复"运维助手暂时过载稍后再试"
//   - 其它 error：四次尝试都失败（网络 / API / 解析）
func (k *Kimi) GenerateOpsSQL(ctx context.Context, question string) (string, error) {
	if k == nil {
		return "", errors.New("kimi 未启用")
	}
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		return "", ErrQuotaCooling
	}
	primaryModel := conf.Cfg.Gpt.RecognizeModel
	fallbackModelStr := conf.Cfg.Gpt.OpsFallbackModel
	// 优先用 Context Cache 省 system prompt token；失效 / 创建失败时 fallback 老路
	cacheID := k.opsSQLCache.loadID()
	req := openai.ChatCompletionRequest{
		Model:       primaryModel,
		Messages:    buildOpsSQLMessages(cacheID, question),
		Temperature: 0.2,
		// 默认 1024 太小（一个稍复杂的 SQL + 注释就接近这个量了），统一 4096
		MaxTokens: 4096,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	}
	resp, err := k.cli.CreateChatCompletion(ctx, req)
	if err != nil && cacheID != "" && k.opsSQLCache.dropIfMissing(err, k.cli) {
		req.Messages = buildOpsSQLMessages("", question)
		resp, err = k.cli.CreateChatCompletion(ctx, req)
	}
	// ② 主模型同型号重试（1.5s 退避）
	if err != nil && IsRetryableError(err) {
		zaplog.Logger.Infof("GenerateOpsSQL 主模型(%s)临时错重试一次: %v", primaryModel, err)
		time.Sleep(1500 * time.Millisecond)
		resp, err = k.cli.CreateChatCompletion(ctx, req)
	}
	// ③ 主模型仍 retryable → 降级到 fallback 模型再尝试一次。
	// K2 在 SQL 生成这种重推理任务上稳定踩 engine_overloaded，v1-auto 这种稳定模型基本不会。
	if err != nil && IsRetryableError(err) && fallbackModelStr != "" && fallbackModelStr != primaryModel {
		zaplog.Logger.Warnf("GenerateOpsSQL 主模型(%s)持续过载，降级到 %s 再试: %v",
			primaryModel, fallbackModelStr, err)
		time.Sleep(1 * time.Second)
		fallbackReq := req
		fallbackReq.Model = fallbackModelStr
		// 降级模型不一定有同样的前缀缓存，cacheID 强制清掉走老路
		fallbackReq.Messages = buildOpsSQLMessages("", question)
		resp, err = k.cli.CreateChatCompletion(ctx, fallbackReq)
	}
	globalQuotaGate.RecordResult(err)
	if err != nil {
		return "", fmt.Errorf("调用 LLM completions 失败: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("LLM 返回 0 choices")
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

// RegenerateOpsSQL 把"上一版 SQL + 数据库报错"喂回 LLM，让它基于错误修正后重新输出一段 SQL。
//
// 调用场景：GenerateOpsSQL 写出的 SQL 在 hfut 端执行时被 PostgreSQL 拒（语法错 / 列名错 /
// 类型转换错等），运维 controller 抓到 err 后调本方法把错误信息回灌给 Kimi，让它自己识别
// 并修正——避免运维同事再手动重新提问一次。
//
// 实现：复用 GenerateOpsSQL 的整条链路（Context Cache + 短退避重试 + JSON 输出约束），
// 只是把"原问题 / 失败 SQL / 数据库报错"三段拼进 user 消息——system prompt 不变，LLM
// 在原 schema 上下文 + 现场错误下重新输出 {"sql": "..."}。
//
// 调用预算：上层负责限制只调一次（避免连环错误把 token 烧光）。
func (k *Kimi) RegenerateOpsSQL(ctx context.Context, question, badSQL, dbError string) (string, error) {
	if k == nil {
		return "", errors.New("kimi 未启用")
	}
	question = truncateLog(question, 1000)
	badSQL = truncateLog(badSQL, 2000)
	dbError = truncateLog(dbError, 1000)
	fixPrompt := fmt.Sprintf(
		"原问题：%s\n\n"+
			"上一次生成的 SQL 在 PostgreSQL 上执行失败，请基于错误信息修正后重新输出。\n\n"+
			"失败的 SQL：\n%s\n\n"+
			"PostgreSQL 报错：\n%s\n\n"+
			"请仔细分析报错原因，按原本的输出约束（严格 JSON {\"sql\":\"...\"}，无 markdown 围栏）输出修正后的 SQL。",
		question, badSQL, dbError,
	)
	return k.GenerateOpsSQL(ctx, fixPrompt)
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
	primaryModel := conf.Cfg.Gpt.RecognizeModel
	fallbackModelStr := conf.Cfg.Gpt.OpsFallbackModel
	req := openai.ChatCompletionRequest{
		Model: primaryModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: opsSummarySystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "结果上下文（JSON）：" + string(payload)},
		},
		Temperature: 0.4,
		MaxTokens:   2048,
	}
	resp, err := k.cli.CreateChatCompletion(ctx, req)
	// 主模型同型号短退避重试
	if err != nil && IsRetryableError(err) {
		zaplog.Logger.Infof("SummarizeOpsResult 主模型(%s)临时错重试一次: %v", primaryModel, err)
		time.Sleep(1500 * time.Millisecond)
		resp, err = k.cli.CreateChatCompletion(ctx, req)
	}
	// 仍 retryable → 降级到 OpsFallbackModel；总结失败时上层会回退到 mechanicalSummary，
	// 但能拿到 LLM 中文摘要体验更好，所以多一层降级值得。
	if err != nil && IsRetryableError(err) && fallbackModelStr != "" && fallbackModelStr != primaryModel {
		zaplog.Logger.Warnf("SummarizeOpsResult 主模型(%s)持续过载，降级到 %s 再试: %v",
			primaryModel, fallbackModelStr, err)
		time.Sleep(1 * time.Second)
		fallbackReq := req
		fallbackReq.Model = fallbackModelStr
		resp, err = k.cli.CreateChatCompletion(ctx, fallbackReq)
	}
	globalQuotaGate.RecordResult(err)
	if err != nil {
		return "", fmt.Errorf("调用 LLM completions 失败: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("LLM 返回 0 choices")
	}
	return resp.Choices[0].Message.Content, nil
}
