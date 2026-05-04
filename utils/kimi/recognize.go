// Package kimi 的 recognize.go 提供"群聊消息 → 业务动作"识别能力。
//
// 跟 Chat() 完全独立：
//   - 不走 QAS 历史（每次识别是无状态的，不该污染聊天上下文）
//   - 不走 tool calling（识别场景输入输出明确，强制 JSON output 比模型自己决定调啥稳）
//   - 不走 read_skill（system prompt 里把规则讲清楚就行，再多查一次 skill 浪费 token + 增加漂的概率）
//   - Temperature 调低（识别要稳定，不要发散）
//
// 设计文档：skill/bot/SKILL.md 的"业务动作识别"段。
package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/northes/go-moonshot"
)

// RecognizeMsg 单条扁平化的群消息，喂给 LLM 用。
//
// 跟 logic.autoReplyMsg 是一一对应的"对外简化版"——只保留模型要看的字段：
//   - MessageID：模型在 ImageMessageIDs / SourceMessageIDs 里要回引这个 ID
//   - Time："HH:MM:SS" 字符串，让模型有时间感知（"刚发"还是"几分钟前"）
//   - Segments：每个段一个字符串描述，文字直接给、图片给 "[图片]"，
//     具体 URL 留在 bot 内部、模型不需要看（避免 URL 干扰判断）
type RecognizeMsg struct {
	MessageID int64    `json:"message_id"`
	Time      string   `json:"time"`
	Segments  []string `json:"segments"`
}

// RecognizeInput 一个"窗口快照"的完整输入——同一发送者在某群的一段连续消息。
type RecognizeInput struct {
	GroupID  int64          `json:"group_id"`
	UserID   int64          `json:"user_id"`
	UserCard string         `json:"user_card"`
	Messages []RecognizeMsg `json:"messages"`
}

// RecognizeAction 模型识别出来的一条业务动作。
//
// 一个 input 里可能 0 个、1 个或多个 action（用户在窗口里发了多商品）。
//
// Type 取值（必填）：
//   - "publish_good"      上架商品（含二手 + 有偿求助；用 Category 区分）
//   - "publish_question"  上架提问
//   - "publish_answer"    针对群内某条提问提交回答
//   - "off_shelf"         下架自己之前挂的商品
//   - "close_question"    关闭自己之前发的提问
//   - "none"              不是业务动作（闲聊 / 噪声 / 不确定都归这里）
type RecognizeAction struct {
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"` // 0~1，模型对自己判定的置信度
	Reason     string  `json:"reason"`     // 一句话解释为什么这么判（人类可读）

	// 上架商品 / 有偿求助
	Title       string   `json:"title,omitempty"`        // 商品标题（短）
	Description string   `json:"description,omitempty"`  // 详细描述（可长）
	Price       *float64 `json:"price,omitempty"`        // 价格；nil 视为未提供（配合 Negotiable）
	Negotiable  bool     `json:"negotiable,omitempty"`   // true = 用户没明确价格 / 写"面议"
	Category    int      `json:"category,omitempty"`     // 1=二手 2=有偿求助
	Location    string   `json:"location,omitempty"`     // 地点（"新区" / "下铺" / 见面地等）；没明确就空

	// 上架提问
	QuestionTitle   string `json:"question_title,omitempty"`
	QuestionContent string `json:"question_content,omitempty"`

	// 提交回答（针对群里别人最近的提问）
	AnswerHintTo  string `json:"answer_hint_to,omitempty"` // 回答的是哪条提问的关键词，bot 后续按这个 hint 在 hfut 里 list_recent_open_questions 找 parent
	AnswerContent string `json:"answer_content,omitempty"`

	// 下架 / 关闭提问的关键词
	OffShelfHint      string `json:"off_shelf_hint,omitempty"`      // "鞋架"、"U型枕"等；空 = 用户没指明，bot 应反问消歧
	CloseQuestionHint string `json:"close_question_hint,omitempty"` // 同上

	// 引用
	ImageMessageIDs  []int64 `json:"image_message_ids,omitempty"`  // 该动作关联的图片消息 ID（用于绑商品图）
	SourceMessageIDs []int64 `json:"source_message_ids,omitempty"` // 该动作来自哪些消息（含主文本 + 图）
}

// RecognizeResult 识别整段窗口后的结果。
type RecognizeResult struct {
	Actions []RecognizeAction `json:"actions"`
}

// recognizeSystemPrompt 给 Kimi 的 system 角色 prompt。
//
// 核心原则：
//   - 严格 JSON 输出（外层 {"actions": [...]}），不要解释、不要 markdown
//   - 保守判断：吃不准就用 type="none"（宁可漏不可错）
//   - 单条消息可能含多个动作，多个动作并列为 actions 数组里的多项
//   - 图片归属：根据时间 / 文字邻近性，把图片消息 ID 关联到最相关的动作
const recognizeSystemPrompt = `你是 QQ 群聊业务消息识别器。给你一段同一发送者的连续 QQ 群消息（已按时间排好），
你要判断里面有哪些"业务动作"，并以严格 JSON 形式输出结果。

## 5 类业务动作

| Type | 触发场景 | 关键字段 |
|---|---|---|
| publish_good     | 用户卖二手 / 发起有偿求助。例:"出鞋架6元"、"代课30r" | title, price?, negotiable, category(1二手/2有偿求助), location?, description?, image_message_ids? |
| publish_question | 用户向群里发起提问。例:"有人有形势与政策题库吗"、"问下大家XX在哪买" | question_title, question_content?, image_message_ids? |
| publish_answer   | 用户在回复群里**别人最近**的提问 | answer_hint_to(被回答的提问关键词), answer_content |
| off_shelf        | 用户表示自己之前的商品已经卖出/不卖了。例:"已出"、"鞋架已出"、"不卖了" | off_shelf_hint(关键词，没指明就空字符串) |
| close_question   | 用户表示自己之前的提问已经解决/不需要了。例:"已找到"、"题库已找到" | close_question_hint |
| none             | 闲聊 / 噪声 / 模糊不清 / 信息不全到没法落库 | reason 给一句话说明判定理由 |

## 关键规则

1. **保守第一**: 不确定就用 type=none。宁可漏掉真的上架，也不要把"今天5块吃了顿饭"识别成商品。
2. **严格 JSON**: 输出必须是 {"actions": [...]}, 不要任何解释文字、不要 markdown 包装。
3. **置信度**: confidence 在 [0,1]，主观给。低于 0.6 的强烈建议改 type=none。
4. **多动作**: 一个用户在窗口里可能上架 2 个商品，分别作为 actions 数组的两项；逐项独立填字段。
5. **图片归属**:
   - 用户群里典型模式："发图 → 紧接着发文字描述"。把图片 message_id 关联到时间上最近的、最相关的那条动作。
   - 找不到关联文字的孤立图片不要单独形成动作（type=none，reason 写"孤立图片无业务文本"）。
6. **价格判定**:
   - 明确数字（"6元"、"15r"、"6 块"）→ price = 6.0；negotiable=false
   - 写了"面议"、"看心情"、"私聊价"、根本没说价 → price 不填；negotiable=true
   - 区间价（"5-10"）→ 取下限作为 price，description 里说明"5-10元"
7. **category 判定**:
   - "出 XX"、"卖 XX"、"二手 XX" → category=1（二手）
   - "代 XX"、"求人 XX"、"找人帮 XX"、带"代课/代取/代买/有偿"等关键词 → category=2（有偿求助）

   ⚠️ **有偿求助的核心是"雇佣关系"**——A 付钱给 B，让 B 替 A 完成一件事（代课、代取、跑腿、代写）。
   下列情况**不是有偿求助**，应归为 type=none：
   - **AA 制活动召集**（"出去玩，人均 20r"、"组队打球，每人 10 元"、"火锅 AA"）：钱是大家分摊费用，
     不存在"雇主→雇员"的服务关系
   - **拼团 / 拼车**（"拼车去机场，AA 一人 30"、"拼单 XX，差一个"）：协作分摊，不是雇佣
   - **众筹 / 凑钱礼物**（"给 XX 凑生日礼物"）：不是服务

   判定捷径：问"谁是雇主、谁要替谁做什么事"——找不到清晰雇佣方向就 type=none。
8. **location**:
   - 用户提到的地点直接抄进去（"新区"、"39栋下铺"、"南区门口"等）
   - 没提就空字符串
9. **off_shelf_hint / close_question_hint**:
   - 用户明确说哪个商品已出（"鞋架已出"）→ hint 写"鞋架"
   - 用户没指明（"已出"、"已找到"）→ hint 写空字符串；bot 后续会反问消歧
10. **time 字段**只是辅助你判断"刚才发"和"几分钟前发"的时间感，不要在输出里复读时间。
11. **source_message_ids**: 每个 action 必填，列出所有用于这次判定的消息 ID（含主文本 + 关联图片）。

## 输出格式（严格 JSON，不要任何额外内容）

{"actions": [
  {
    "type": "publish_good",
    "confidence": 0.92,
    "reason": "用户明确说'出'+物品+价格",
    "title": "三层鞋架",
    "price": 6.0,
    "negotiable": false,
    "category": 1,
    "location": "",
    "image_message_ids": [10001],
    "source_message_ids": [10001, 10002]
  }
]}

如果整段都没有任何业务动作，返回 {"actions": []} 即可。`

// RecognizeBusinessActions 把一段窗口快照交给 Kimi 识别业务动作。
//
// 调用方应当保证 k != nil；nil 接收者会 panic。
func (k *Kimi) RecognizeBusinessActions(ctx context.Context, input RecognizeInput) (*RecognizeResult, error) {
	if k == nil {
		return nil, errors.New("kimi 未启用")
	}

	// 把 input 序列化成模型 user message 里的 JSON 文本——比组装中文文本更结构化、token 更稳
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("RecognizeInput 序列化失败: %w", err)
	}

	resp, err := k.cli.Chat().Completions(ctx, &moonshot.ChatCompletionsRequest{
		Model: moonshot.ModelMoonshotV1128K,
		Messages: []*moonshot.ChatCompletionsMessage{
			{Role: moonshot.RoleSystem, Content: recognizeSystemPrompt},
			{Role: moonshot.RoleUser, Content: "请识别下面这段窗口的业务动作:\n" + string(inputJSON)},
		},
		Temperature: 0.2, // 低温度——识别任务要稳定，不要发散
		ResponseFormat: &moonshot.ChatCompletionsRequestResponseFormat{
			Type: moonshot.ChatCompletionsResponseFormatJSONObject,
		},
		// 不传 Tools——识别不需要 tool calling
	})
	if err != nil {
		return nil, fmt.Errorf("调用 moonshot completions 失败: %w", err)
	}
	msg, err := resp.GetMessage()
	if err != nil {
		return nil, fmt.Errorf("从 moonshot 响应取 message 失败: %w", err)
	}

	var result RecognizeResult
	if err := json.Unmarshal([]byte(msg.Content), &result); err != nil {
		return nil, fmt.Errorf("识别结果 JSON 解析失败: %w, raw=%s", err, msg.Content)
	}
	return &result, nil
}
