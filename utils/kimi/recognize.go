// Package kimi 的 recognize.go 提供"群聊消息 → 业务动作"识别能力。
//
// 跟 Chat() 完全独立：
//   - 不走 QAS 历史（每次识别是无状态的，不该污染聊天上下文）
//   - 不走 tool calling（识别场景输入输出明确，强制 JSON output 比模型自己决定调啥稳）
//   - 不走 read_skill（system prompt 里把规则讲清楚就行，再多查一次 skill 浪费 token + 增加漂的概率）
//   - Temperature 调低（识别要稳定，不要发散）
//
// 设计文档：skill/bot/SKILL.md 的"5 类业务动作"段 + skill/bot/recognition.md "Kimi prompt hard rules"。
package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"qq_bot/conf"

	"github.com/northes/go-moonshot"
)

// ErrQuotaCooling 是识别入口在 quotaGate 冷却期内 short-circuit 时返回的 sentinel。
//
// 调用方（auto_reply）应当用 errors.Is 检测，再决定是退化为 regex 兜底还是直接 drop。
// 这条错误**不算** "识别失败" log——是预期内的熔断行为，应该 INFO 级别。
var ErrQuotaCooling = errors.New("kimi quota gate cooling")

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
	Title       string   `json:"title,omitempty"`       // 商品标题（短）
	Description string   `json:"description,omitempty"` // 详细描述（可长）
	Price       *float64 `json:"price,omitempty"`       // 价格；nil 视为未提供（配合 Negotiable）
	Negotiable  bool     `json:"negotiable,omitempty"`  // true = 未说价或明确「面议」；与 price=0 免费送不同
	Category    int      `json:"category,omitempty"`    // 1=二手 2=有偿求助
	Location    string   `json:"location,omitempty"`    // 地点（"新区" / "下铺" / 见面地等）；没明确就空

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
| publish_good     | 用户卖二手 / 发起有偿求助 / AA 制活动召集 / 拼车拼团。例:"出鞋架6元"、"代课30r"、"出去玩 人均20" | title, price?, negotiable, category(1二手/2有偿求助及活动召集), location?, description?, image_message_ids? |
| publish_question | 用户向群里发起**有信息密度的、值得长期归档到 app 提问区**的提问。例:"有人有形势与政策题库吗"、"问下大家计算机学院XX课在哪买教材"、"3栋热水房几点开" | question_title, question_content?, image_message_ids? |
| publish_answer   | 用户在回复群里**别人最近**的提问 | answer_hint_to(被回答的提问关键词), answer_content |
| off_shelf        | 用户表示自己之前的商品已经卖出/不卖了。例:"已出"、"鞋架已出"、"不卖了" | off_shelf_hint(关键词，没指明就空字符串) |
| close_question   | 用户表示自己之前的提问已经解决/不需要了。例:"已找到"、"题库已找到" | close_question_hint |
| none             | 闲聊 / 噪声 / 模糊不清 / 信息不全到没法落库 | reason 给一句话说明判定理由 |

## 关键规则

1. **保守第一**: 不确定就用 type=none。宁可漏掉真的上架，也不要把"今天5块吃了顿饭"识别成商品。
2. **严格 JSON**: 输出必须是 {"actions": [...]}, 不要任何解释文字、不要 markdown 包装。
3. **置信度**: confidence 在 [0,1]，主观给。低于 0.6 的强烈建议改 type=none。
4. **多动作**: 一个用户在窗口里可能上架 2 个商品，分别作为 actions 数组的两项；逐项独立填字段。
5. **图片归属**（双向关联，图在文前 / 文在图前都要识别）:
   - 群里典型有两种模式：
     a) "[图片] → 出 XX 5元"——先发图后发文（旧模式）
     b) "出 XX 5元 → [图片]"——先发文后发图（也很常见）
     c) 多个商品交错："[图1] [图1] 出 A 5元 [图2] 出 B 10元"——按时间紧邻关系切两组动作
   - 关联原则：图片归到**时间上最近的、且语义相关**的那条文字描述上，**不限制方向**（图在前后都行）。
   - 找不到关联文字的孤立图片不要单独形成动作（type=none，reason 写"孤立图片无业务文本"）。
   - 同一动作可关联多张连续图（一个商品多张实拍）。
6. **价格判定**:
   - 明确数字（"6元"、"15r"、"6 块"）→ price = 6.0；negotiable=false
   - 明确「0」「0元」「免费」「白送」「不要钱」「无偿」→ price = 0.0；negotiable=false（≠ 面议）
   - 写了"面议"、"看心情"、"私聊价"、**完全没说价** → price 字段不输出；negotiable=true
   - 区间价（"5-10"）→ 取下限作为 price，description 里说明"5-10元"，negotiable=false
7. **category 判定**:

   category=1 = "卖东西/出东西板块"——发布者把**自己持有的东西**给别人，换钱。
   category=2 = "有偿求助 / 多人协作板块"——发布者**付钱**让别人帮忙做事 / 大家分摊。

   **强信号关键字（**硬规则**）**：

   - 发布者**主动**说"出 XX"、"卖 XX"、"转让 XX"、"赠/送（带价格）" → **必然** category=1，
     无论 XX 是不是传统二手物品（汤粉 / 自制食物 / 票 / 闲置全 OK）
   - 发布者主动说"代 XX"、"求代 XX"、"找人 XX"、"求帮 XX 多少钱" → **必然** category=2

   分歧来源：**"卖食物"也是 category=1**。"出一碗汤粉 8r"、"卖自制蛋糕"、"转让多余水果"
   都是发布者把现成的东西给别人，是 category=1，**不是**有偿求助。
   "求代取一份汤粉 5r" / "代点食堂二楼汤粉" 才是 category=2（"我没汤粉，找人帮我搞来"）。

   category=2 具体覆盖：
   - **雇佣型求助**："代课 30r"、"代取快递 5r"、"找人帮带饭 5r"——付钱让人做事
   - **AA 制活动召集**："出去玩，人均 20r"、"组队打球，每人 10 元"、"火锅 AA"——大家分摊
   - **拼车 / 拼团**："拼车去机场，AA 一人 30"、"拼单 XX，差一个"——协作型分摊

   判定捷径：发布者是"提供物品方"还是"花钱方"？
   - 发布者是**提供方**（卖出 / 转让自己有的东西） → category=1
   - 发布者是**花钱方**（付钱让别人做事 / 分摊别人的活动） → category=2

   下列情况仍然 type=none（不上架）：
   - **众筹 / 凑钱礼物**（"给 XX 凑生日礼物"）：私人活动，不算交易
   - 单纯抱怨涉及金钱（"今天 5 块吃饭也太贵了"）
   - 文字游戏 / 玩笑 / 调侃（"30 块买个寂寞"）
   - 完全没有金额信息且不是明确二手卖（不知道谁要付钱给谁）
8. **publish_question 严格判定**（重点防误识别为提问）:

   提问区是"长期归档、其它学生能搜索到、能写回答的求助板块"——不是 QQ 群闲聊。
   提问识别的**核心准入门槛**：必须**同时**满足下列**全部**条件，否则 **强制 type=none**。

   ### 必要条件（缺一不可）

   1. **有可被搜索的具体"主题词"** —— 学科名 / 课程名 / 地点名（栋号/校区/食堂/具体设施）/
      物品名 / 资源名 / 服务名等专有信息。**没有就不是提问**——例如：
      - ❌ "为什么没货"、"怎么这样"、"咋办"——抽象问句，没主题词
      - ❌ "有人吗"、"在吗"——招呼，没主题词
      - ✅ "大物B 题库"、"8栋热水房"、"形势与政策习题"——主题词具体

   2. **是用户主动起的"独立提问"**，不是回应别人刚才说的话 —— 看消息上下文：
      - 群里 1 分钟内有人晒商品 / 发链接 / 聊天 → 后续短问句几乎都是闲聊回应，**强制 none**
      - 用户自己第 1 条消息就是这个问题 → 才可能是真提问

   3. **正文 ≥ 10 个汉字** 或 **含明显正式问法**（"有人有 XX 吗"、"求问 XX"、"问下大家 XX"、
      "请问 XX"等模板化开头）—— 短问句 + 没正式开头 = 闲聊。
      - ❌ "为什么没货"（5 字 + 没正式开头）
      - ❌ "怎么买啊"、"哪买的"（短 + 跟前文互动）
      - ✅ "请问大家有大物 B 题库吗谢谢"（≥10 字 + "请问"开头）

   4. **不是情绪 / 玩笑 / 抱怨语气** —— "啊？"、"真的吗"、"???"、"咋这么贵"、"离谱"等
      永远 type=none。

   ### Hard reject（无视图片、无视长度，直接 type=none）

   下列模式不管满足什么都**不**算提问：
   - 以"为什么"、"怎么"、"咋"、"哪"、"啊"开头的短句（< 10 字）—— 99% 是聊天回应
   - 配图但文本 < 10 字（"[图片] 这是什么"、"为什么没货[图]"）—— 图片不能"补救"短文本，主题词得在文字里
   - 单字 / 表情 / 重复标点（"？"、"啊？？"、"[糖笑]"）

   ### 反例（**必须** type=none）

   - "为什么没货" + 图——短问句 + 没主题词 + 是回应前面消息（hard reject）
   - "怎么买啊" / "在哪买"——跟在别人卖货后面的互动，不是独立提问
   - "啊？"、"真的吗"、"???"
   - "在吗"、"有人吗"
   - "[图片] 这是什么"

   ### 正例（可以 type=publish_question）

   - "有人有大物 B 期末复习题库吗，谢谢"——主题词"大物B 题库" + 正式问法 + 长度 14 字
   - "问下大家 8 栋有没有自助洗衣"——主题词"8栋 自助洗衣" + "问下大家" 模板开头 + 长度 12 字
   - "求问 [图] 这门课在哪买教材，老师上课只放了一页"——主题词"教材 / 学科图" + "求问"开头 + 上下文充足

9. **location**:
   - 用户提到的地点直接抄进去（"新区"、"39栋下铺"、"南区门口"等）
   - 没提就空字符串
10. **off_shelf_hint / close_question_hint**:
    - 用户明确说哪个商品已出（"鞋架已出"）→ hint 写"鞋架"
    - 用户没指明（"已出"、"已找到"）→ hint 写空字符串；bot 后续会反问消歧
11. **time 字段**只是辅助你判断"刚才发"和"几分钟前发"的时间感，不要在输出里复读时间。
12. **source_message_ids**: 每个 action 必填，列出所有用于这次判定的消息 ID（含主文本 + 关联图片）。
13. **多图 / 文字主导原则**（适用于所有 type，不仅 publish_question）:

    - 即便用户在窗口里发了多张 [图片]，**判断的依据始终是文字诉求**——图片是辅助佐证，不能凭空"补救"缺失的诉求文本。
    - 反例（即便有 5 张图也必须 type=none）：
      - "[图1] [图2] [图3]"（纯图，无文字）
      - "[图1] 看看"、"[图1] 这个怎么样"（短互动，无具体诉求）
    - 正确处理：图片归到最近的、带具体诉求的那条文字；找不到归属的图片**不要**单独形成 action。

14. **撤回 / 改主意**（Hard reject 列表）:

    用户在同一窗口里**先说要发布、紧接着改主意撤回**——不要识别为 publish_*：

    - "算了不卖了"、"刚才那个不算"、"忽略我刚才说的"、"撤回上一条" → 全段 type=none，reason 写"用户撤回上一条意图"。
    - 如果撤回前的诉求确实是"已发布过的商品/提问"——那是 off_shelf / close_question，按已有规则识别。
    - 不要替用户做"撤回 + 重新上架"这种二步操作；只识别用户**最终**留下的诉求。

15. **不指代具体物品的"出"语句**（防误识别）:

    - "出了" / "都出了" / "拿出来" / "出门" 这些**没有商品名 + 没有价格**的句子，**不**算 publish_good 也不算 off_shelf。
    - 真要识别为 off_shelf 至少需要：商品名 / 类目词 / "刚才那个" 等明确指代之一。

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
//
// 错误模式：
//   - ErrQuotaCooling：quotaGate 处于冷却期，未实际调 API；上层应改走 RecognizeViaRegex 兜底
//   - 其它 error：网络 / API / parse 错——上层应 ack=fail（normal 模式静默）
func (k *Kimi) RecognizeBusinessActions(ctx context.Context, input RecognizeInput) (*RecognizeResult, error) {
	if k == nil {
		return nil, errors.New("kimi 未启用")
	}
	// 进入 quota 冷却期则 short-circuit——不再撞 API、让上层走 regex 兜底
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		return nil, ErrQuotaCooling
	}

	// 把 input 序列化成模型 user message 里的 JSON 文本——比组装中文文本更结构化、token 更稳
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("RecognizeInput 序列化失败: %w", err)
	}

	model := moonshot.ChatCompletionsModelID(conf.Cfg.Gpt.RecognizeModel)
	resp, err := k.cli.Chat().Completions(ctx, &moonshot.ChatCompletionsRequest{
		Model: model,
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
	globalQuotaGate.RecordResult(err)
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
