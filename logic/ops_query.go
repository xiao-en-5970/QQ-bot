// Package logic 的 ops_query.go —— 运维群"自然语言提问 → 只读 SQL"链路。
//
// 触发：在 conf.Bot.OpsGroupIDs 任意一个群里 @bot 提问，文本会进本路径。
// 不在 OpsGroup 的群仍然走原本的 @bot 命令分发（jm / pix / chat 等）。
//
// 流程：
//
//  1. 提取 @bot 之后的纯文本问题
//  2. 调 kimi.GenerateOpsSQL → JSON {"sql": "..."}
//  3. 调 hfut.RunAdminSQL（POST /api/v1/bot/admin/sql）执行
//  4. 调 kimi.SummarizeOpsResult 把结果总结成一句中文
//  5. 群里发回："SQL\n────\n中文总结"
//
// 失败兜底：
//
//   - kimi 没启用 / quota cooling → "运维助手暂时离线，请稍后再试"
//   - SQL 静态检查 / 执行失败 → 群里把 hfut 返回的错误文案直接发出来（带 SQL）
//   - 总结失败 → 直接发"已查到 N 行（具体 SQL: …）"，不阻塞
//
// 安全边界（最重要的几条）：
//
//   - **入口必须 IsOpsGroup 二次校验**：handle_at_message.go 已经分流过，但 HandleOpsQuery
//     再校验一次防御性 return——任何代码路径意外调用本函数，都会静默 return 不暴露
//     能力存在；非 OpsGroup 用户绝不可能触发任何 SQL 相关回复。
//   - hfut 那边 BotServiceAuth + 静态白名单 + PG READ ONLY tx + 10s timeout + LIMIT 200
//     四道防线；本路径不重新校验 SQL（信任 hfut）
//   - 限速：复用 dispatch 的"per-(group, user)"令牌桶（在 dispatch.go 中），
//     避免运维群里有人狂刷 API 耗光 Kimi quota / DB 连接
//   - 错误回复脱敏：不在群里输出具体 env key 名 / 内部组件名，避免泄露架构细节给
//     可能被加入运维群的非核心成员（运维群成员不一定 100% 可信）
package logic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/model"
	"qq_bot/utils/hfut"
	"qq_bot/utils/kimi"
	zaplog "qq_bot/utils/zap"
)

// HandleOpsQuery 在运维群 @bot 时被调用，处理自然语言查询。
//
// 防御性约束（与 handle_at_message.go 的分流互为冗余）：
//   - 群必须在 conf.Bot.IsOpsGroup；不是的话**直接静默 return**，
//     绝不向群里发任何回复——包括"权限不足"提示也不发，避免暴露能力存在。
//   - msg.GroupID == 0（私聊）也静默 return；本能力只对群开放。
//
// 调用方应该已经确认：
//   - 第 0 段是 @bot、第 1 段是 text 且 text 已 trim
func HandleOpsQuery(client *http.Client, msg *model.Message, question string) {
	if msg == nil || msg.GroupID == 0 {
		return // 静默：不在任何群上下文里
	}
	// 防御性二次校验——即便 handle_at_message.go 已经过滤，这里再守一道
	if !conf.Cfg.Bot.IsOpsGroup(msg.GroupID) {
		zaplog.Logger.Warnf("ops_query 被非运维群调用，已静默 group=%d user=%d", msg.GroupID, msg.UserID)
		return // 绝不向非运维群发任何回复
	}

	q := strings.TrimSpace(question)
	if q == "" {
		_ = SendGroupAtText(client, msg.GroupID, msg.UserID, "请在 @ 我之后写下你的问题，例如 \"近 1 小时接口请求量\"")
		return
	}

	// 依赖 sanity check——只对运维同事说"暂时不可用"，不暴露具体配置 key 名
	if global.Kimi == nil || global.Hfut == nil {
		_ = SendGroupAtText(client, msg.GroupID, msg.UserID, "运维助手暂时不可用，请联系部署方")
		return
	}

	zaplog.Logger.Infof("ops_query group=%d user=%d question=%q", msg.GroupID, msg.UserID, truncateForLog(q, 200))

	// 第 1 步：LLM 生 SQL
	genCtx, cancelGen := context.WithTimeout(context.Background(), 30*time.Second)
	sqlStr, err := global.Kimi.GenerateOpsSQL(genCtx, q)
	cancelGen()
	if err != nil {
		if errors.Is(err, kimi.ErrQuotaCooling) {
			_ = SendGroupAtText(client, msg.GroupID, msg.UserID, "运维助手过载（Kimi 冷却中），请稍后再试")
			return
		}
		zaplog.Logger.Warnf("ops_query generate sql 失败: %v", err)
		_ = SendGroupAtText(client, msg.GroupID, msg.UserID, "生成 SQL 失败："+truncateForReply(err.Error(), 200))
		return
	}
	sqlStr = strings.TrimSpace(stripCodeFence(sqlStr))
	zaplog.Logger.Infof("ops_query group=%d user=%d sql=%q", msg.GroupID, msg.UserID, truncateForLog(sqlStr, 400))

	// 第 2 步：调 hfut 执行
	execCtx, cancelExec := context.WithTimeout(context.Background(), 15*time.Second)
	res, err := global.Hfut.RunAdminSQL(execCtx, sqlStr, 200)
	cancelExec()
	if err != nil {
		zaplog.Logger.Warnf("ops_query exec sql 失败 sql=%q: %v", truncateForLog(sqlStr, 200), err)
		_ = SendGroupAtText(client, msg.GroupID, msg.UserID,
			fmt.Sprintf("SQL 执行失败：%s\n\nSQL:\n%s", truncateForReply(err.Error(), 200), sqlStr))
		return
	}

	// 第 3 步：LLM 总结。失败不阻塞，回退到机械摘要
	sumCtx, cancelSum := context.WithTimeout(context.Background(), 30*time.Second)
	summary, sErr := global.Kimi.SummarizeOpsResult(sumCtx, q, sqlStr, res.Columns, res.Rows)
	cancelSum()
	if sErr != nil {
		zaplog.Logger.Warnf("ops_query summarize 失败 (回退机械摘要): %v", sErr)
		summary = mechanicalSummary(res)
	}

	// 拼最终回复：摘要 + SQL + 表头/行数。前 5 行作为示例 dump（仍超 200 行的话只展示前 5）
	reply := composeOpsQueryReply(summary, sqlStr, res)
	_ = SendGroupAtText(client, msg.GroupID, msg.UserID, reply)
}

// composeOpsQueryReply 拼运维群最终消息。
//
// 格式：
//
//	{summary}
//
//	SQL: {sql}
//	返回 {row_count} 行 / 耗时 {elapsed} ms
//	{机械化前 N 行 ascii table，仅列出 columns 和最多 5 行}
//
// QQ 群消息没有 markdown 渲染，纯文本足够运维肉眼判断。
func composeOpsQueryReply(summary string, sqlStr string, res *hfut.AdminSQLResp) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(summary))
	b.WriteString("\n\nSQL: ")
	b.WriteString(strings.TrimSpace(sqlStr))
	fmt.Fprintf(&b, "\n返回 %d 行 / 耗时 %d ms", res.RowCount, res.ElapsedMs)
	if len(res.Rows) > 0 && len(res.Columns) > 0 {
		b.WriteByte('\n')
		b.WriteString(asciiTable(res.Columns, res.Rows, 5))
	}
	return b.String()
}

// mechanicalSummary LLM 总结失败时的兜底文案。
func mechanicalSummary(res *hfut.AdminSQLResp) string {
	if res.RowCount == 0 {
		return "查询执行成功，没有匹配数据。"
	}
	return fmt.Sprintf("查询执行成功，共返回 %d 行结果。", res.RowCount)
}

// asciiTable 把前 max 行 + 列名拼成纯文本表（QQ 群消息能直接看）。
//
// 简单等宽列：列宽 = max(列名长度, 该列在前 max 行内的最长值长度)，最多 24 字符。
func asciiTable(cols []string, rows [][]any, max int) string {
	if len(rows) > max {
		rows = rows[:max]
	}
	const maxW = 24
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = displayWidth(c, maxW)
	}
	for _, row := range rows {
		for i, v := range row {
			w := displayWidth(toCellStr(v), maxW)
			if w > widths[i] {
				widths[i] = w
			}
		}
	}

	var b strings.Builder
	pad := func(s string, w int) {
		s = clipString(s, w)
		b.WriteString(s)
		for k := len(s); k < w; k++ {
			b.WriteByte(' ')
		}
	}
	for i, c := range cols {
		if i > 0 {
			b.WriteString("  ")
		}
		pad(c, widths[i])
	}
	b.WriteByte('\n')
	for _, row := range rows {
		for i, v := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			pad(toCellStr(v), widths[i])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func toCellStr(v any) string {
	if v == nil {
		return "NULL"
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		// 用 json 编码兜底——slice / map / 数字 都能打印
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		s := string(b)
		// 把字符串值的引号去掉，让表格更紧凑
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			s = s[1 : len(s)-1]
		}
		return s
	}
}

func displayWidth(s string, max int) int {
	if len(s) > max {
		return max
	}
	return len(s)
}

func clipString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

// stripCodeFence 兼容 LLM 偶尔仍然包了 ```sql ... ``` 围栏的情况。
func stripCodeFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	t = strings.TrimPrefix(t, "```sql")
	t = strings.TrimPrefix(t, "```SQL")
	t = strings.TrimPrefix(t, "```")
	t = strings.TrimSuffix(t, "```")
	return strings.TrimSpace(t)
}

// truncateForReply 截断要发回 QQ 群的错误文案，过长会被风控误伤。
func truncateForReply(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

