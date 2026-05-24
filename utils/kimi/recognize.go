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
	"time"

	"qq_bot/conf"
	zaplog "qq_bot/utils/zap"

	"github.com/northes/go-moonshot"
)

// ErrQuotaCooling 是识别入口在 quotaGate 冷却期内 short-circuit 时返回的 sentinel。
//
// 调用方（auto_reply）应当用 errors.Is 检测；当前策略是直接静默丢弃当前窗口——
// 正则做语义识别不可靠，宁可漏不可错（详见 skill/bot/recognition.md "LLM 配额熔断" 段）。
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
//   - "seek_goods"        求购/收购/收某物——只检索在售二手，不落库
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
	Bargain     bool     `json:"bargain,omitempty"`     // 可刀：文案含「可刀」「刀」等（与面议独立）
	Category    int      `json:"category,omitempty"`    // 1=二手 2=有偿求助
	Stock       int      `json:"stock,omitempty"`       // 库存数量；用户没明说=0（dispatch 落库时按 1 兜底）
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

	// seek_goods：检索关键词（物品名）
	SeekHint string `json:"seek_hint,omitempty"`

	// 引用
	ImageMessageIDs  []int64 `json:"image_message_ids,omitempty"`  // 该动作关联的图片消息 ID（用于绑商品图）
	SourceMessageIDs []int64 `json:"source_message_ids,omitempty"` // 该动作来自哪些消息（含主文本 + 图）
}

// RecognizeResult 识别整段窗口后的结果。
type RecognizeResult struct {
	Actions []RecognizeAction `json:"actions"`
}

// buildRecognizeMessages 按是否命中 Context Cache 选择 messages 结构。
//
//   - cacheID != ""  → messages = [cache_ref, user]，不传 system；Moonshot 会从
//     cache 里恢复 system prompt。reset_ttl 让缓存跟着请求活跃度续命。
//   - cacheID == ""  → messages = [system(完整 prompt), user]，走老路（首次调用 /
//     cache 创建失败 / cache 失效兜底）。
//
// 单独抽函数方便在 cache 失效 fallback 时复用同一份构造逻辑。
func buildRecognizeMessages(cacheID, inputJSON string) []*moonshot.ChatCompletionsMessage {
	userContent := "请识别下面这段窗口的业务动作:\n" + inputJSON
	if cacheID != "" {
		return []*moonshot.ChatCompletionsMessage{
			cacheReferenceMessage(cacheID),
			{Role: moonshot.RoleUser, Content: userContent},
		}
	}
	return []*moonshot.ChatCompletionsMessage{
		{Role: moonshot.RoleSystem, Content: recognizeSystemPrompt},
		{Role: moonshot.RoleUser, Content: userContent},
	}
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

## 6 类业务动作

| Type | 触发场景 | 关键字段 |
|---|---|---|
| publish_good     | 用户卖二手 / 发起求物品（求帮做事 / 求物品 / AA / 拼车）。例:"出鞋架6元"、"代课30r"、"出去玩 人均20" | title, price?, negotiable, bargain?, category(1二手 / 2求物品), location?, description?, image_message_ids? |
| publish_question | 用户向群里发起**有信息密度的、值得长期归档到 app 求解答板块**的提问。例:"有人有形势与政策题库吗"、"问下大家计算机学院XX课在哪买教材"、"3栋热水房几点开" | question_title, question_content?, image_message_ids? |
| publish_answer   | 用户在回复群里**别人最近**的求解答 | answer_hint_to(被回答的提问关键词), answer_content |
| off_shelf        | 用户表示自己之前的商品已经卖出/不卖了。例:"已出"、"鞋架已出"、"不卖了" | off_shelf_hint(关键词，没指明就空字符串) |
| close_question   | 用户表示自己之前的求解答已经解决/不需要了。例:"已找到"、"题库已找到" | close_question_hint |
| seek_goods       | 用户**想买/想求**某物（**没给具体价**）："收鞋架"、"收「U型枕」"、"求购鼠标"、"收购教材"、"求数值分析PPT"、"求高数复习题"。**dispatch 端会**：① 搜本校在售给提示，② 当作「求物品 + 0 元（前端不展示价格）」帮 ta 上架。**不要**把「收到」「收起」「收录」「收尾」当求购；用户已经给了价（"求 X 5r"）→ 走 publish_good cat=2 而非 seek_goods | seek_hint(物品关键词，必填且具体) |
| none             | 闲聊 / 噪声 / 模糊不清 / 信息不全到没法落库 | reason 给一句话说明判定理由 |

## 关键规则

1. **保守第一**: 不确定就用 type=none。宁可漏掉真的上架，也不要把"今天5块吃了顿饭"识别成商品。
2. **严格 JSON**: 输出必须是 {"actions": [...]}, 不要任何解释文字、不要 markdown 包装。
3. **置信度**: confidence 在 [0,1]，主观给。低于 0.6 的强烈建议改 type=none。
4. **多动作**: 一个用户在窗口里可能上架 2 个商品，分别作为 actions 数组的两项；逐项独立填字段。
5. **图片归属 / 图文交杂多商品**（输入是用户在 60s 内的连续发言整段）:

   bot 已经把"这位用户从开始发言到沉默 60s 之间的所有消息"按时间顺序排好喂给你。
   一段窗口里可能含**多张图 + 多条文 + [视频] + 图文同条**任意组合——不论顺序，
   **整段是这位用户的一次或多次完整上架**，请整体判断。**绝对不要**看到第一段
   "标题文字"就提前结束、把后面的文字（往往是价格 / 描述续写）误归到下一次上架。

   ### 单一商品 vs 多商品的判定

   **关键：先数窗口里有几个"具体商品名 / 主标题"信号**——再决定切几个动作。

   - **单一商品**（最常见）：窗口里只有 1 个商品名信号——即只有一句包含具体物品名
     的文字，其它文字是补充描述 / 价格 / 备注。把整段（所有图 + 所有文）归到 1 个
     publish_good。

     典型例子：用户依次发了 [图片]→[图片]→[图片]→"AOC G2490VX显示器，24寸IPS屏，
     144Hz高刷，1080p分辨率..."→[图片]→"200 可小刀"（中间用户可能还发了视频，bot
     已过滤）。这是**一个**商品（显示器）：title="AOC G2490VX显示器"、price=200、
     bargain=true，所有 [图片] 都归这个动作的 image_message_ids。**不要**把
     "200 可小刀"当成新一次上架——窗口里只有一个商品名信号，"200 可小刀"是它的
     价格续写。

   - **多商品**（用户**显式**列出多个不同物品名）：例如 "出鞋架 6 元、出 U 型枕 5
     元、出杯子 3 元"——这时按文字出现顺序切 N 个动作。

   ### 图文配对（**多商品场景**——单一商品场景所有图都归唯一动作即可，无需配对）

   - 用户图文顺序**一致**：第 i 段图归属第 i 段商品文字
   - 模式 [图 图 ... 文1 图 图 ... 文2]（先图后文）：每段文字归紧邻**前面**的连续图
   - 模式 [文1 图 图 ... 文2 图 图 ...]（先文后图）：每段文字归紧邻**后面**的连续图
   - **图文交杂**（[图 图 文1 图 文2 图 图]）：按"图归属时间上最近的商品文字"原则
     配对，不限制图在文前还是文后

   ### 批量图文一一对应纠错（**多商品 + N ≥ 5 时优先适用，覆盖上面的时间邻近配对**）

   当窗口里出现"较多商品文字 + 数量相当的图片"，并且大多数消息时间戳几乎相同
   （说明是用户"快速连发批量上架"），通常理想模式是严格交替
   [图 文 图 文 图 文 ...]，一图对一文。但用户难免会失误——漏发一张图、连发
   两张图、连续打了两段文字——按"时间邻近"配对会让一处错乱**雪崩**到后面全部
   错位。

   **判定条件**（全部满足才启用本规则）：
     1. 商品文字数 N ≥ 5
     2. 图片数 M 与 N 满足 |M - N| ≤ 2
     3. 窗口里大多数消息时间在同一秒 / 几秒内（用户连发节奏）
     4. 消息不带 [来自聊天记录] 标记（聊天记录走它自己的规则）

   **配对策略**：按"出现顺序号"对齐——商品文字按出现顺序编号 t1..tN，图片按
   出现顺序编号 p1..pM。**第 i 个商品文字归到第 i 张图**（而不是死板按时间相邻）。

   典型例子（实际错位场景）：

     输入: [p1] t1 [p2] t2 [p3] t3 [p4] t4 [p5] t5 t6 [p6] [p7] t7 [p8] t8 [p9] t9 [p10] t10
     错位: t5 后无图（漏）、t6 没自带图、[p6][p7] 连发两图

     **错的做法**（时间邻近）：t6 抢走 p6，t7 抢走 p7，t8 抢走 p8…全部错位
     **对的做法**（按顺序号）：
       t1↔p1, t2↔p2, t3↔p3, t4↔p4, t5↔p5
       t6 在序号上没有对应的图（M=10, t6 是第 6 个文字，p6/p7 在序号上属于 t6/t7
       中的某一个；优先把 p6 留给 t6——它的图被用户漏发了；p7 给 t7）
       t7↔p7, t8↔p8, t9↔p9, t10↔p10

     具体到边界：N == M 时严格一对一；M < N 时把多出的文字 image_message_ids 留
     空（说明用户漏发了图）；M > N 时把多出的图视为某条文字的补充（追加到时间上
     最近的那条文字的 image_message_ids）。

     **不要**因为某条文字没有紧邻的图就把它跟下一张图配对——那样会把后面所有商品
     全错位。

   ### 图文同条（QQ 输入框最常见）

   同一个 message_id 下 segments 数组里既含文字也含 [图片]，例如下面三种 segments
   排列（文在前 / 图在前 / 图文交错）都是**同一个 case**：

     ["出5个二次元图片","[图片]","[图片]","[图片]","[图片]","[图片]"]
     ["[图片]","[图片]","[图片]","[图片]","[图片]","出5个二次元图片"]
     ["[图片]","出5个二次元图片","[图片]","[图片]"]

   QQ 输入框允许 inline 混排，整条消息是用户一次"图文一起发"的完整上架意图。要求：
   该 message_id **同时**出现在 image_message_ids 与 source_message_ids 中；
   segments 里有几个 [图片] 占位就绑几张图。

   ### 孤立图 / 表情包

   - 找不到关联文字的**孤立**图片不要单独形成动作（type=none，reason 写"孤立图片
     无业务文本"）。**注意**：在单一商品场景里，所有图都归唯一动作，不算孤立。
   - 视觉上明显是**表情包 / 梗图**的 [图片] 按"无关图"处理，**不要**绑到任何商品
     的 image_message_ids。bot 给你的输入只有 [图片] 占位符看不到真图，**靠你结合
     时间间隔 + 上下文**判断：图与商品文字之间有较长沉默 / 文字也没说"看图"时视为
     无关图。

   ### [face] / [reply]

   桶里的 [face] / [reply] **不影响**上架判定——是发布过程中的修饰物，忽略即可。
   （bot 已经从输入里彻底过滤了视频段，所以你不会看到 [视频] 之类的占位符；用户
   发的视频在 app 端不展示，bot 不需要让你感知。）

   ### 聊天记录展开（重要）

   用户在群里转发"合并聊天记录"时，bot 已经把记录里的每条子消息**逐条展开**喂给你；
   每条这种消息的 segments **以 [来自聊天记录] 标记开头**。这种消息有几个关键
   特性，跟普通群消息差别很大：

   1. **每条来自聊天记录的消息，单独就是一次独立的发布行为**——是用户在过去某时刻
      独立发的，**不应该跟其它消息合并成同一个商品**。
   2. 即便整窗口里所有消息都带 [来自聊天记录] 标记，也**不要**把它们合并成"图文
      交杂的一次上架"——单一商品判定那条规则**不适用**。这是用户转发了 N 条独立
      上架的聊天记录，应识别成 **N 个** publish_good。
   3. 每条独立判定：标记后**该消息的 segments**（[图片] / 商品文字 / 价格）就是一个
      完整商品的信息。多个 [图片] 跟着 1 段文字 = 这一条消息内部的图文同条上架。
   4. image_message_ids / source_message_ids 只填**当前这条消息的 message_id**
      ——绝对不要把其它带 [来自聊天记录] 的消息 ID 跨条混入。

   典型例子（用户转发了 3 段独立上架的聊天记录）：

     消息1（21:30:15）：[来自聊天记录] [图片] [图片] 出鞋架 6 元
     消息2（21:30:42）：[来自聊天记录] [图片] 出 U 型枕 5 元
     消息3（21:31:05）：[来自聊天记录] [图片] [图片] 出自行车 200 元

   正确识别：**3 个** publish_good 动作，分别 title="鞋架"/price=6, title="U型枕"/
   price=5, title="自行车"/price=200。每个 action 的 image_message_ids /
   source_message_ids **只包含自己那一条**消息的 message_id。

   反例（**不要**这样判）：把 3 条合并成一个 publish_good，title 写成"鞋架/U型枕/
   自行车" 或随便挑一个——这是错的，等于丢失了另外两个商品。
6. **价格判定**（区分"明确面议" vs "完全没说价"）:
   - 明确数字（"6元"、"15r"、"6 块"）→ price = 6.0；negotiable=false
   - 明确「0」「0元」「免费」「白送」「不要钱」「无偿」→ price = 0.0；negotiable=false（≠ 面议）
   - 用户**显式**写了"面议"、"看心情"、"私聊价"、"价格私"等 → price 字段不输出；**negotiable=true**
   - **完全没说价**（既没数字也没"面议"等关键词）：
     - **category=1（二手）**：默认 negotiable=true，price 不输出（卖东西没标价默认让人私聊）
     - **category=2（求物品）**：**price 不输出**，**negotiable=false**（dispatch 当 0 元处理，前端不展示价格、不挂"有偿"tag）
   - 区间价（"5-10"）→ 取下限作为 price，description 里说明"5-10元"，negotiable=false
   - 「可刀」「可小刀」「刀」「让刀」等表示接受砍价 → bargain=true（可与明码标价同时为 true）
7. **category 判定**:

   category=1 = "二手板块"——发布者把**自己持有的东西**给别人，换钱。
   category=2 = "求物品板块（含求物 / 求帮做事 / AA / 拼车）"——发布者求一件东西或求人帮忙；
              带价 = 有偿；不带价 = 单纯求物。

   **强信号关键字（**硬规则**，优先级从上到下）**：

   - **"出/卖/转让 XX"系列优先级最高**：发布者**主动**说"出 XX"、"卖 XX"、"转让 XX"、
     "赠/送（带价格）" → **必然** category=1，**与 XX 内部的字眼无关**。
     即便 XX 本身包含"代 / 求 / 收 / 帮"等字眼（如"出代练"、"出代肝"、"出代写"、
     "出代购名额"、"出代抓宝"、"出帮带"），仍然是 category=1——
     "出"这个动词锁定了发布者是**提供方**，"代肝/代练" 等只是被提供的**服务名**。
     Rule of thumb：动词前缀 = "出/卖/转让" → cat=1；不管后面跟什么名词都按 cat=1。
   - 发布者主动说"求代 XX"、"找代 XX"、"找人 XX"、"求帮 XX 多少钱"、"招人 XX"等求助方动词
     起头 → **必然** category=2。注意必须是动词起头（求/找/招/求帮），单独的"代 XX"前面没有
     求助动词时，要看上下文判断；如果整句句首就是"代 XX"且没有"出/卖"作为前缀，按 cat=2 处理。
   - **隐式上架（默认 category=1）**：图片 + 物品名 + 价格组合（"[图片]\n笔记本支架臂，50r"、
     "[图片] 雀巢咖啡 30 元"），但句子里**没有**任何"求 / 拼 / 收 / 买"等求助方动词，
     **必然** category=1——这是用户在晒自己的东西+标价的典型卖家样式，不要错判为 category=2。
     即便没有图，纯文字"物品名 + 价格"且没有动词前缀（"按压U型枕 5元"），也按 category=1 处理。

   分歧来源：**"卖食物"也是 category=1**。"出一碗汤粉 8r"、"卖自制蛋糕"、"转让多余水果"
   都是发布者把现成的东西给别人，是 category=1，**不是**求物品。
   "求代取一份汤粉 5r" / "代点食堂二楼汤粉" 才是 category=2（"我没汤粉，找人帮我搞来"）。

   category=2 具体覆盖：
   - **雇佣型求助**："代课 30r"、"代取快递 5r"、"找人帮带饭 5r"——付钱让人做事
   - **AA 制活动召集**："出去玩，人均 20r"、"组队打球，每人 10 元"、"火锅 AA"——大家分摊
   - **拼车 / 拼团**："拼车去机场，AA 一人 30"、"拼单 XX，差一个"——协作型分摊

   判定捷径：发布者是"提供物品方"还是"花钱方"？
   - 发布者是**提供方**（卖出 / 转让自己有的东西、主动提供服务） → category=1
   - 发布者是**花钱方**（付钱让别人做事 / 分摊别人的活动） → category=2

   特别区分（关键易错样例）：
   - "出代练 50r"  → cat=1（我能代练，付费来找我）
   - "出代肝 30r"  → cat=1（我能代肝，付费来找我）
   - "出代写论文" → cat=1（我能代写，找我）
   - "求代练 50r" → cat=2（我要找人代练）
   - "找代肝"     → cat=2（我要找人代肝）
   - "求代购"     → cat=2（我要找人代购）
   - "代取快递 5r"（句首"代"+动词宾语+价格）→ cat=2（用户付钱让人帮做）
   - "出代取快递服务" → cat=1（我能代取，付费来找我）

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

9b. **stock（商品数量）**:
   - 用户明说数量的，输出 stock 整数，**且 title 里不要保留数字+量词**——只留商品本身的名字：
     - "出 3 个鞋架 5r/个" → title="鞋架", stock=3
     - "鞋架×2 共 10 元" → title="鞋架", stock=2
     - "出 5 张电影票" → title="电影票", stock=5
     - "出5个二次元图片[图片]" → title="二次元图片", stock=5（即便"数字+量词+商品名"中间**没有空格**也要拆出来）
     - "卖2包零食" → title="零食", stock=2
     - "出3个鞋架" → title="鞋架", stock=3
     - "拼车 3 个座位" → title="拼车", stock=3
   - 常见量词包括："个 / 张 / 件 / 份 / 套 / 本 / 盒 / 包 / 瓶 / 把 / 双 / 块 / 条 / 只 / 颗 / 粒 / 杯 / 袋 / 串 / 台 / 部 / 副 / 支"等——出现"动词+数字+量词+商品名"格式就拆出 stock 与商品名。
   - 用户没明说数量 → **不输出 stock 字段**（dispatch 端按 1 兜底）
   - 数量 ≤ 0 / 非整数 / 看不出来 → 不输出 stock 字段（按 1 兜底）
   - 数量超过 999 视为夸张表达 / 看错 → 不输出 stock（按 1 兜底，**保守起见**）
10. **off_shelf_hint / close_question_hint**:
    - 用户明确说哪个商品已出 / 已求得（"鞋架已出"、"教材已求到"）→ hint 写"鞋架"/"教材"
    - 用户没指明（短句单独成立："已出"、"已找到"、"求到了"、"已求得"、"已买到"、"是" 表示已卖出/已求到）→ hint 写空字符串；bot 后续反问消歧
    - 用户明确否定未出（"不是"、"没出"、"还没求到"、"还需要"）→ type=none，不要 off_shelf
    - **二手 vs 求物品 关键词差异**（用户回复 bot 的"请求下架"问句时）：
      - 二手（cat=1）→ 已出 / 出掉了 / 是
      - 求物品（cat=2）→ 已找到 / 找到了 / 已求到 / 求到了 / 已求得 / 已买到 / 是
      - 两类都映射到同一个 off_shelf action（hfut 后端按 good 自身的 category 决定下架 / 关闭求购）
11. **time 字段**只是辅助你判断"刚才发"和"几分钟前发"的时间感，不要在输出里复读时间。
12. **source_message_ids**: 每个 action 必填，列出所有用于这次判定的消息 ID（含主文本 + 关联图片）。
13. **多图 / 文字主导原则**（适用于所有 type，不仅 publish_question）:

    - 即便用户在窗口里发了多张 [图片]，**判断的依据始终是文字诉求**——图片是辅助佐证，不能凭空"补救"缺失的诉求文本。
    - **上架信号至少要满足下列两条之一**（即"动词信号 OR 价格信号"，缺一也行，两个都没就要警惕）：
      a) **动词信号**：句中有"出/卖/转让/求/收/求购/找/招/求帮/招人"等明确的发布或求购**动词前缀**。
      b) **价格信号**：句中有明确价格关键词（数字 + 元/r/块、"免费"、"0 元"、"面议"、"私聊价"等）。
      两者都没有时，多图 + 物品名只是"用户晒图"，**不要**脑补成 publish_good 或 seek_goods。
    - 隐式上架（适用 a 或 b 任一成立）正例（**保留**为 publish_good）：
      - "[图1]…[图N] 网易云 5r"——有价格 → publish_good cat=1（典型卖家晒图样式）
      - "[图1] 雀巢咖啡 30 元"、"[图1] 按压U型枕 5元"——有价格 → publish_good cat=1
      - "[图1] 出鞋架"——有动词 → publish_good cat=1（即便没价）
    - 反例（即便有 5 张甚至 13 张图也必须 type=none，因为**既无动词、又无价格**）：
      - "[图1] [图2] [图3]"（纯图，无文字）
      - "[图1] 看看"、"[图1] 这个怎么样"（短互动，无具体诉求）
      - **"[图1]…[图N] 来点 X"、"[图1]…[图N] 想要 X"、"[图1]…[图N] 给我来点 X"、"[图1]…[图N] 想看 X"、"[图1]…[图N] 求张 X"**——这是用户在群里**讨内容看**的口语，不是商品发布也不是求购，统一 type=none，reason="用户在求看内容（来点 / 想要 / 想看 X）非交易意图"。
      - 反例延伸："来点猫图"、"想要点壁纸"、"给我来几张表情包"、"求张高清原图"——只要是「**来 / 来点 / 想要 / 想看 / 给我来 / 求张** + 名词」的"讨内容"语气，且**没有价格**，全部 type=none。
    - 正确处理：图片归到最近的、带具体诉求的那条文字；找不到归属的图片**不要**单独形成 action。

14. **撤回 / 改主意 / 取消**:

    分两类：

    a) **Hard reject**（窗口内先发布后撤回）——用户**当前**这次发言里既包含发布意图、又紧接着改主意撤回：

       - "算了不卖了"、"刚才那个不算"、"忽略我刚才说的"、"撤回上一条"、"算了改主意了" 等 → 全段 type=none，reason 写"用户撤回上一条意图"
       - 不要替用户做"撤回 + 重新上架"这种二步操作；只识别用户**最终**留下的诉求

    b) **Standalone 撤销**（用户**只**发了这几个字，没新的发布意图）——映射为 off_shelf：

       - 单独短句 "不要了" / "不卖了" / "不出了" / "不需要了" → type=**off_shelf**，off_shelf_hint=空字符串
       - 语义：撤销 ta 自己最近一次成功上架/求物品（bot 会用 hint=空 + 最近发布上下文找到具体那条）
       - 用户回到 cat=1（二手）：意为"我刚才说要卖的不卖了" → off_shelf
       - 用户回到 cat=2（求物品）：意为"我刚才求的不需要了" → 同样 off_shelf（dispatch 端按 good 自身 category 处理）

    c) 历史已发布商品的撤销（"鞋架已出"、"题库已找到"、"教材已求到"）按规则 10 走 off_shelf，已经覆盖。

15. **不指代具体物品的"出"语句**（防误识别）:

    - "出了" / "都出了" / "拿出来" / "出门" 这些**没有商品名 + 没有价格**的句子，**不**算 publish_good 也不算 off_shelf。
    - 真要识别为 off_shelf 至少需要：商品名 / 类目词 / "刚才那个" 等明确指代之一。

16. **seek_goods（求物品，未给价）**:

    - 触发词：**收 / 收购 / 求购 / 求** + **具体物品名**（至少 2 字或明确名词）。seek_hint 只写物品名，不要带动词。
    - 用户已经写了价（如 "求 X 5r"、"收 X 10元"） → **走 publish_good cat=2** 而不是 seek_goods（让 price 字段生效，前端会挂"有偿"tag）。
    - **Hard reject**：「收到」「收到了」「收起」「收录」「收尾」等明显不是求购 → type=none。
    - 与 publish_good(category=1) 互斥：用户在卖东西（出/卖+价）→ 不要 seek_goods。
    - dispatch 端会**同时**做：① 搜本校在售给提示；② 当作"求物品 cat=2、price=0、negotiable=false"帮 ta 上架（前端不展示价格、也不挂"有偿"tag）。

17. **物品名一律不甄别**（绝不做合规 / 真实性 / 道德判断）:

    bot 只判断"用户有没有上架/求购的**意图**"，**不**判断"X 这件商品/服务该不该存在"。

    用户上架的物品可能是：
    - 玩笑 / 指代（"上架3个小孩，10r一个"、"卖自家弟弟"）
    - 游戏道具 / 圈内黑话（名字怪是常态："收奶刀打孔"、"出红温白嫖号"）
    - 自创品 / 不通用名词（"出我做的手工"、"收特调奶茶"）
    - 含争议字眼（"出花呗额度"、"代写论文"、"考试答案"）
    - 看似抽象但其实指代具体物（"出时间"= 陪聊服务、"卖青春"= 二手日记本）

    上面**全部正常识别**为 publish_good / seek_goods——只要用户表达了"出/卖/收/求 + 名词"
    的交易意图就识别，title/seek_hint 直接抄用户原话。合规判断 / 平台政策 / 校规审核
    完全交给后端 / 人工，bot 不替代。

    bot 仅在以下情况返回 type=none（**这些都是"没有上架意图"的情况**，跟物品本身无关）：

    - 纯闲聊 / 噪声 / 跟交易完全不沾边的对话
    - 信息不全到没法落库（只有 "出" 没说出什么，且周围也没图）
    - 全是占位符 / 表情 / 引用 / @ ，没文字也没图
    - 模型自己也看不出在表达什么（不是因为"X 不是物品"而 none）

    Rule of thumb：用户**有没有交易意图**？
    - 有（说了出/卖/收/求 + 名词） → 一律识别，不评判 X 是什么
    - 没有（闲聊 / 招呼 / 玩笑没动词 / 信息不全） → type=none

## 输出格式（严格 JSON，不要任何额外内容）

{"actions": [
  {
    "type": "publish_good",
    "confidence": 0.92,
    "reason": "用户明确说'出'+物品+价格",
    "title": "三层鞋架",
    "price": 6.0,
    "negotiable": false,
    "bargain": false,
    "category": 1,
    "location": "",
    "image_message_ids": [10001],
    "source_message_ids": [10001, 10002]
  }
]}

### 图文同条 + 多数量示例

输入消息（注意只有 1 条 message_id，segments 里既有文字也有 5 个 [图片] 占位符）：

  {"message_id": 1792241695, "time": "15:46:45",
   "segments": ["出5个二次元图片","[图片]","[图片]","[图片]","[图片]","[图片]"]}

正确输出：

{"actions": [
  {
    "type": "publish_good",
    "confidence": 0.95,
    "reason": "图文同条上架；'出5个X'拆为 title=X, stock=5",
    "title": "二次元图片",
    "negotiable": true,
    "category": 1,
    "stock": 5,
    "image_message_ids": [1792241695],
    "source_message_ids": [1792241695]
  }
]}

### 反例：多图 + "来点 X" / "想要 X" 是讨内容，**不是**上架

输入消息（13 张图，紧跟一句 4 字"来点儿童画"，**没有任何**发布/求购动词）：

  [
    {"message_id": 100, "time": "01:04:25", "segments": ["[图片]"]},
    {"message_id": 101, "time": "01:04:25", "segments": ["[图片]"]},
    {"message_id": 102, "time": "01:04:26", "segments": ["[图片]"]},
    ...（共 13 条 [图片]）...
    {"message_id": 113, "time": "01:04:36", "segments": ["来点儿童画"]}
  ]

正确输出（**必须** type=none，不要因为"图片+物品名"就脑补成 publish_good）：

{"actions": [
  {
    "type": "none",
    "confidence": 0.92,
    "reason": "用户在群里讨内容看（来点 X）非交易意图：无出/卖/转让/求/收等发布或求购动词，文字本身只是讨儿童画看的口语"
  }
]}

同样要 type=none 的反例：

- "[图1]…[图N] 来点猫图"
- "[图1]…[图N] 想要点壁纸"
- "[图1]…[图N] 给我来几张表情包"
- "[图1]…[图N] 求张高清原图"（"求张"也是讨内容，不是 seek_goods——seek_goods 要"求购 / 收购"等明确求购意图）

如果整段都没有任何业务动作，返回 {"actions": []} 即可。`

// RecognizeBusinessActions 把一段窗口快照交给 Kimi 识别业务动作。
//
// 调用方应当保证 k != nil；nil 接收者会 panic。
//
// 错误模式：
//   - ErrQuotaCooling：quotaGate 处于冷却期，未实际调 API；上层应当**直接静默**，不要做语义兜底
//     （正则做语义识别不可靠，会污染落库数据；宁可漏不可错）
//   - 其它 error：网络 / API / parse 错——上层应 ack=fail（normal 模式静默）
func (k *Kimi) RecognizeBusinessActions(ctx context.Context, input RecognizeInput) (*RecognizeResult, error) {
	if k == nil {
		return nil, errors.New("kimi 未启用")
	}
	// 进入 quota 冷却期则 short-circuit——不再撞 API、由上层直接静默丢弃
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		return nil, ErrQuotaCooling
	}

	// 把 input 序列化成模型 user message 里的 JSON 文本——比组装中文文本更结构化、token 更稳
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("RecognizeInput 序列化失败: %w", err)
	}

	model := moonshot.ChatCompletionsModelID(conf.Cfg.Gpt.RecognizeModel)

	// 优先用 Moonshot Context Cache：把超长的 recognizeSystemPrompt 注册成命名缓存
	// 后只塞一条 cache 引用 + 重置 TTL，省 80%+ 的 system message 输入 token 费。
	// 详见 context_cache.go。fallback 走老路：每次发完整 system prompt。
	cacheID := k.recognizeCache.loadID()
	messages := buildRecognizeMessages(cacheID, string(inputJSON))

	req := &moonshot.ChatCompletionsRequest{
		Model:       model,
		Messages:    messages,
		Temperature: 0.2, // 低温度——识别任务要稳定，不要发散
		// Moonshot SDK 默认 max_tokens=0 表示走服务端默认（1024），实测当用户一次
		// 发了 10+ 个商品（每个商品 ~300 tokens JSON）时会被截断、返回 finish_reason=length，
		// 上层 json.Unmarshal 报 unexpected end of JSON input。把上限拉到 8192，
		// 覆盖到 20+ 商品的极端场景；moonshot-v1-auto / kimi-k2 都支持。
		MaxTokens: 8192,
		ResponseFormat: &moonshot.ChatCompletionsRequestResponseFormat{
			Type: moonshot.ChatCompletionsResponseFormatJSONObject,
		},
		// 不传 Tools——识别不需要 tool calling
	}
	resp, err := k.cli.Chat().Completions(ctx, req)
	// cache 失效特例：Moonshot 返回 "cache not found" / "invalid cache" 类错误时，
	// 清掉内存 cache_id（后台重建）并退化到老路重试一次——避免本次识别整个失败。
	if err != nil && cacheID != "" && k.recognizeCache.dropIfMissing(err, k.cli) {
		req.Messages = buildRecognizeMessages("", string(inputJSON))
		resp, err = k.cli.Chat().Completions(ctx, req)
	}
	// 一次性短退避重试：engine_overloaded / 5xx / 网络瞬抖大多 800ms 后好。
	// 重试只针对 retryable 错误（IsQuotaError 仍立刻返回，让 quotaGate 走熔断）。
	if err != nil && IsRetryableError(err) {
		zaplog.Logger.Infof("RecognizeBusinessActions 临时错重试一次: %v", err)
		time.Sleep(800 * time.Millisecond)
		resp, err = k.cli.Chat().Completions(ctx, req)
	}
	globalQuotaGate.RecordResult(err)
	if err != nil {
		return nil, fmt.Errorf("调用 moonshot completions 失败: %w", err)
	}
	msg, err := resp.GetMessage()
	if err != nil {
		return nil, fmt.Errorf("从 moonshot 响应取 message 失败: %w", err)
	}

	// finish_reason=length 表示输出被 max_tokens 截断；JSON 一定不完整，直接报错
	// 走 ack=fail（或 normal 静默），避免下游 json.Unmarshal 报 unexpected EOF 的劣质日志。
	if len(resp.Choices) > 0 && resp.Choices[0].FinishReason == moonshot.FinishReasonLength {
		return nil, fmt.Errorf("识别结果被 max_tokens=%d 截断（finish_reason=length），用户内容过多，请考虑分批发送", req.MaxTokens)
	}

	var result RecognizeResult
	if err := json.Unmarshal([]byte(msg.Content), &result); err != nil {
		return nil, fmt.Errorf("识别结果 JSON 解析失败: %w, raw=%s", err, msg.Content)
	}
	return &result, nil
}
