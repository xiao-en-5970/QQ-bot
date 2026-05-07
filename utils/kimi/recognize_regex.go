// Package kimi 的 recognize_regex.go 提供 LLM 不可用时的"保底正则识别"。
//
// 触发条件：quotaGate 处于冷却期 / LLM 出现 ErrQuotaCooling 时，由 logic 层调用本文件。
// 设计原则：**保守 + 可解释**——只识别两类置信度极高的场景，宁可漏掉也不允许错触发。
//
// 与 LLM 路径的关键差别：
//   - 仅看单条消息（不做跨条窗口聚合；规避语义聚合的歧义）
//   - 仅识别 publish_good（含二手/求助两类）+ off_shelf；问答类一律 drop
//   - 必须有强锚定特征（价格 / 关键词）才命中，避免"算了不卖了"被错判
//   - confidence 统一打 0.6，上层 ack 可据此提示用户"模型暂时不可用，机器人用关键词识别的，如不对请回'撤销'"
//
// SKILL：跟 skill/bot/recognition.md "Kimi prompt hard rules" 对齐——尤其是
// rule 14（撤回 hard reject）与 rule 15（无价格的"出"句子不算 publish_good）。

package kimi

import (
	"regexp"
	"strconv"
	"strings"
)

// regexFallbackConfidence 兜底识别的固定置信度——比 LLM 平均值低，让 ack 层能区分。
const regexFallbackConfidence = 0.6

// 撤回 / 改主意：只要句子里出现这些词，整条 input 直接 drop（hard reject，对应 hard rule 14）。
//
// 不做精确分词；用 strings.Contains 简单粗暴——regex 兜底场景下宁可漏识别也不要误识别。
var regexHardRejectKeywords = []string{
	"算了", "刚才那个不算", "忽略我刚才", "撤回上一条", "不卖了", "不出了", "不要了", "改主意",
}

// publish_good 二手买卖：头部锚定 + 价格强制
//
// 命中样例：
//   - "出三层鞋架 6元"
//   - "出 按压U型枕 5 元"
//   - "卖 自行车 200块"
//   - "出鞋架 6r"
//
// 不命中（保守，避免 hard rule 15 失效）：
//   - "出了"、"都出了"、"出门"、"拿出来"
//   - "出 不知道"（标题为空）
//   - "出 鞋架 面议"（无数值价）：由 rePublishGoodSecondHandNegotiable 兜底
var rePublishGoodSecondHand = regexp.MustCompile(
	`^\s*(?:出|卖)\s*([\p{Han}\p{L}\p{N} ・·、，,。；;]{1,40}?)\s*(\d+(?:\.\d{1,2})?)\s*[元r块￥¥]`,
)

// publish_good 二手：「出/卖 + 标题 + 面议」（无数值）
var rePublishGoodSecondHandNegotiable = regexp.MustCompile(
	// 不用 \b：`面议` 后接行尾或标点更稳（Go 的 \b 对中文词尾常不成立）
	`^\s*(?:出|卖)\s*([\p{Han}\p{L}\p{N} ・·、，,。；;]{1,40}?)\s*面议(?:\s*[!！。\.]*)?\s*$`,
)

// publish_good 有偿求助：头部锚定 "代/求人/拼/求带" + 价格强制
//
// 命中样例：
//   - "代课 30r"
//   - "代写 50元"
//   - "求人帮带饭 5元"
//   - "拼车去机场 30 AA"（也匹配，AA 不强制价格在前）
//
// 不命中：
//   - "求 XX"（求资源、求经验）—— 这是 publish_question，不在 regex 兜底范围
var rePublishGoodHelp = regexp.MustCompile(
	`^\s*(?:代|求人|拼|求带)\s*([\p{Han}\p{L}\p{N} ・·、，,。；;]{1,40}?)\s*(\d+(?:\.\d{1,2})?)\s*[元r块￥¥]`,
)

// publish_good 有偿求助：「代/求人/拼/求带 + 标题 + 面议」（无数值）
var rePublishGoodHelpNegotiable = regexp.MustCompile(
	`^\s*(?:代|求人|拼|求带)\s*([\p{Han}\p{L}\p{N} ・·、，,。；;]{1,40}?)\s*面议(?:\s*[!！。\.]*)?\s*$`,
)

// off_shelf：必须以"已出"或"已找到"为关键词，否则 drop
//
// 三种命中模式：
//   - 整句就是"已出" / "已找到"  → hint=空，上层走消歧
//   - "鞋架已出" / "三层鞋架已出"  → hint=鞋架
//   - "已出 鞋架"                  → hint=鞋架
var (
	reOffShelfPlain    = regexp.MustCompile(`^\s*(?:已出|已找到|找到了|出掉了)\s*[!！。\.]?\s*$`)
	reOffShelfWithHint = regexp.MustCompile(
		`^\s*(?:([\p{Han}\p{L}\p{N} ・·]{1,30})\s*(?:已出|已找到|出掉了)|(?:已出|已找到)\s*([\p{Han}\p{L}\p{N} ・·]{1,30}))\s*[!！。\.]?\s*$`,
	)
)

// RecognizeViaRegex 在 LLM 不可用（quota 冷却 / 显式 fallback）时做最低保障识别。
//
// 输入是跟 LLM 路径完全相同的 RecognizeInput；输出是 RecognizeResult（actions 可能为空）。
// 不返回 error——任何匹配不到 / 解析失败都退化为 actions=空，让上层 ack 层去 drop。
//
// 关键约束：
//   - **只看 input.Messages 里每条单消息的纯文本拼接**，不做跨条聚合（聚合识别留给 LLM）
//   - 命中 hard reject 关键词整条 input 直接返回空
//   - 同一条消息至多产出 1 个 action（避免一句话里同时识别为上架+下架）
func RecognizeViaRegex(input RecognizeInput) *RecognizeResult {
	out := &RecognizeResult{}
	for _, msg := range input.Messages {
		text := joinSegmentsForRegex(msg.Segments)
		if text == "" {
			continue
		}
		if containsHardReject(text) {
			// 整条 input 都 drop——遵循 hard rule 14
			return &RecognizeResult{}
		}

		if act, ok := tryPublishGood(text, msg.MessageID); ok {
			out.Actions = append(out.Actions, applyBargainFromText(text, act))
			continue
		}
		if act, ok := tryOffShelf(text, msg.MessageID); ok {
			out.Actions = append(out.Actions, act)
			continue
		}
	}
	return out
}

// joinSegmentsForRegex 把一条消息的 segments 拼接成单行纯文本——图片段忽略。
//
// 多行换行会被合并成单空格（让上面的"^...$"行锚定还能工作）；图片段位置记号
// "[图片]" 也丢掉（regex 兜底只看文字）。
// applyBargainFromText 文案含「可刀」等时打上 bargain（保守：不匹配孤立「刀」字以免误伤）
func applyBargainFromText(text string, act RecognizeAction) RecognizeAction {
	for _, kw := range []string{"可刀", "可小刀", "刀一下", "让刀", "可议价"} {
		if strings.Contains(text, kw) {
			act.Bargain = true
			break
		}
	}
	return act
}

func joinSegmentsForRegex(segs []string) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" || s == "[图片]" {
			continue
		}
		s = strings.ReplaceAll(s, "\n", " ")
		parts = append(parts, s)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// containsHardReject 子串匹配硬否定关键词。
func containsHardReject(text string) bool {
	for _, kw := range regexHardRejectKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// tryPublishGood 尝试匹配二手 / 有偿求助两套正则，命中即组装 action。
//
// 二手优先匹配；二手不命中再试求助——避免"代课 30元"被二手正则吃掉（实际不会，
// 二手要求"出/卖"开头，但顺序上还是二手优先以保持"出鞋架 30元"的稳定性）。
func tryPublishGood(text string, msgID int64) (RecognizeAction, bool) {
	if m := rePublishGoodSecondHand.FindStringSubmatch(text); len(m) == 3 {
		title := strings.TrimSpace(m[1])
		priceYuan, err := strconv.ParseFloat(m[2], 64)
		if err != nil || title == "" {
			return RecognizeAction{}, false
		}
		// 单价上限 100w 元——超出大概率是误识别（"出 房产 1000000元"这类不该 regex 兜底）
		if priceYuan > 1_000_000 {
			return RecognizeAction{}, false
		}
		return RecognizeAction{
			Type:             "publish_good",
			Confidence:       regexFallbackConfidence,
			Reason:           "regex 兜底（二手）：'出/卖 + 标题 + 价格' 模式命中",
			Title:            title,
			Description:      text,
			Price:            &priceYuan,
			Negotiable:       false,
			Category:         1,
			SourceMessageIDs: []int64{msgID},
		}, true
	}
	if m := rePublishGoodHelp.FindStringSubmatch(text); len(m) == 3 {
		title := strings.TrimSpace(m[1])
		priceYuan, err := strconv.ParseFloat(m[2], 64)
		if err != nil || title == "" {
			return RecognizeAction{}, false
		}
		if priceYuan > 100_000 {
			return RecognizeAction{}, false
		}
		return RecognizeAction{
			Type:             "publish_good",
			Confidence:       regexFallbackConfidence,
			Reason:           "regex 兜底（求助）：'代/求人/拼/求带 + 标题 + 价格' 模式命中",
			Title:            title,
			Description:      text,
			Price:            &priceYuan,
			Negotiable:       false,
			Category:         2,
			SourceMessageIDs: []int64{msgID},
		}, true
	}
	if m := rePublishGoodSecondHandNegotiable.FindStringSubmatch(text); len(m) == 2 {
		title := strings.TrimSpace(m[1])
		if title == "" || containsHardReject(text) {
			return RecognizeAction{}, false
		}
		return RecognizeAction{
			Type:             "publish_good",
			Confidence:       regexFallbackConfidence,
			Reason:           "regex 兜底（二手）：'出/卖 + 标题 + 面议'",
			Title:            title,
			Description:      text,
			Price:            nil,
			Negotiable:       true,
			Category:         1,
			SourceMessageIDs: []int64{msgID},
		}, true
	}
	if m := rePublishGoodHelpNegotiable.FindStringSubmatch(text); len(m) == 2 {
		title := strings.TrimSpace(m[1])
		if title == "" || containsHardReject(text) {
			return RecognizeAction{}, false
		}
		return RecognizeAction{
			Type:             "publish_good",
			Confidence:       regexFallbackConfidence,
			Reason:           "regex 兜底（求助）：'代/求人/拼/求带 + 标题 + 面议'",
			Title:            title,
			Description:      text,
			Price:            nil,
			Negotiable:       true,
			Category:         2,
			SourceMessageIDs: []int64{msgID},
		}, true
	}
	return RecognizeAction{}, false
}

// tryOffShelf 尝试匹配 "已出" 三种形态。命中后 hint 空 = 让上层走消歧反问；
// 有 hint = 直接传给 OffShelfGood 工具，由 hfut 端模糊匹配。
func tryOffShelf(text string, msgID int64) (RecognizeAction, bool) {
	if reOffShelfPlain.MatchString(text) {
		return RecognizeAction{
			Type:             "off_shelf",
			Confidence:       regexFallbackConfidence,
			Reason:           "regex 兜底：'已出/已找到' 单独短句命中",
			OffShelfHint:     "",
			SourceMessageIDs: []int64{msgID},
		}, true
	}
	if m := reOffShelfWithHint.FindStringSubmatch(text); len(m) == 3 {
		hint := strings.TrimSpace(m[1])
		if hint == "" {
			hint = strings.TrimSpace(m[2])
		}
		if hint == "" {
			return RecognizeAction{}, false
		}
		return RecognizeAction{
			Type:             "off_shelf",
			Confidence:       regexFallbackConfidence,
			Reason:           "regex 兜底：'XX 已出' 或 '已出 XX' 模式命中",
			OffShelfHint:     hint,
			SourceMessageIDs: []int64{msgID},
		}, true
	}
	return RecognizeAction{}, false
}

// IsRegexFallback 让上层判断一个 action 是不是 regex 兜底产出的——置信度等于
// regexFallbackConfidence 视为是。
//
// 用于 ack 层在文案上区分："已为你上架 X" vs "已用关键词识别上架 X，如不对回'撤销'"。
func IsRegexFallback(act *RecognizeAction) bool {
	if act == nil {
		return false
	}
	return act.Confidence == regexFallbackConfidence
}
