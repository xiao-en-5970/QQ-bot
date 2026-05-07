package kimi

import (
	"testing"
)

// helper：单条文字消息进 RecognizeInput
func singleMsg(text string) RecognizeInput {
	return RecognizeInput{
		GroupID:  100,
		UserID:   200,
		UserCard: "tester",
		Messages: []RecognizeMsg{
			{MessageID: 999, Time: "12:00:00", Segments: []string{text}},
		},
	}
}

func TestRegex_PublishGood_SecondHand(t *testing.T) {
	cases := []struct {
		text      string
		wantTitle string
		wantPrice float64
	}{
		{"出三层鞋架 6元", "三层鞋架", 6},
		{"出 按压U型枕 5 元", "按压U型枕", 5},
		{"卖自行车 200块", "自行车", 200},
		{"出鞋架 6r", "鞋架", 6},
		{"  出  老物件  18.5 元  ", "老物件", 18.5},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			res := RecognizeViaRegex(singleMsg(c.text))
			if len(res.Actions) != 1 {
				t.Fatalf("应识别为 1 条 action，得到 %d: %+v", len(res.Actions), res.Actions)
			}
			a := res.Actions[0]
			if a.Type != "publish_good" {
				t.Errorf("type = %q, want publish_good", a.Type)
			}
			if a.Title != c.wantTitle {
				t.Errorf("title = %q, want %q", a.Title, c.wantTitle)
			}
			if a.Price == nil || *a.Price != c.wantPrice {
				t.Errorf("price = %v, want %v", a.Price, c.wantPrice)
			}
			if a.Category != 1 {
				t.Errorf("category = %d, want 1", a.Category)
			}
			if a.Confidence != regexFallbackConfidence {
				t.Errorf("confidence = %v, want %v", a.Confidence, regexFallbackConfidence)
			}
			if !IsRegexFallback(&a) {
				t.Errorf("IsRegexFallback 应当 true")
			}
		})
	}
}

func TestRegex_PublishGood_Help(t *testing.T) {
	cases := []struct {
		text      string
		wantTitle string
		wantPrice float64
	}{
		{"代课 30r", "课", 30},
		{"代写实验报告 50元", "写实验报告", 50}, // "代" 被当作动词，标题从下一字开始
		{"求人帮带饭 5元", "帮带饭", 5},
		{"拼车去机场 30元", "车去机场", 30},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			res := RecognizeViaRegex(singleMsg(c.text))
			if len(res.Actions) != 1 {
				t.Fatalf("应识别为 1 条，得到 %d: %+v", len(res.Actions), res.Actions)
			}
			a := res.Actions[0]
			if a.Type != "publish_good" || a.Category != 2 {
				t.Errorf("应识别为 publish_good category=2，得到 type=%q category=%d", a.Type, a.Category)
			}
			if a.Title != c.wantTitle {
				t.Errorf("title = %q, want %q", a.Title, c.wantTitle)
			}
			if a.Price == nil || *a.Price != c.wantPrice {
				t.Errorf("price = %v, want %v", a.Price, c.wantPrice)
			}
		})
	}
}

func TestRegex_PublishGood_Negotiable(t *testing.T) {
	cases := []struct {
		text           string
		wantTitle      string
		wantCategory   int
		wantNegotiable bool
	}{
		{"出 鞋架 面议", "鞋架", 1, true},
		{"卖自行车 面议", "自行车", 1, true},
		{"代取快递 面议", "取快递", 2, true},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			res := RecognizeViaRegex(singleMsg(c.text))
			if len(res.Actions) != 1 {
				t.Fatalf("应识别为 1 条 action，得到 %d: %+v", len(res.Actions), res.Actions)
			}
			a := res.Actions[0]
			if a.Type != "publish_good" {
				t.Errorf("type = %q, want publish_good", a.Type)
			}
			if a.Title != c.wantTitle {
				t.Errorf("title = %q, want %q", a.Title, c.wantTitle)
			}
			if a.Category != c.wantCategory {
				t.Errorf("category = %d, want %d", a.Category, c.wantCategory)
			}
			if a.Negotiable != c.wantNegotiable {
				t.Errorf("negotiable = %v, want %v", a.Negotiable, c.wantNegotiable)
			}
			if a.Price != nil {
				t.Errorf("price = %v, want nil", a.Price)
			}
		})
	}
}

func TestRegex_PublishGood_RejectAmbiguous(t *testing.T) {
	// hard rule 15：没有商品名 + 没有价格的"出"句子不算 publish_good
	rejects := []string{
		"出了",
		"都出了",
		"出门",
		"拿出来",
		"出鞋架", // 没价格、没「面议」
		"6 元", // 没"出"前缀
	}
	for _, txt := range rejects {
		t.Run(txt, func(t *testing.T) {
			res := RecognizeViaRegex(singleMsg(txt))
			if len(res.Actions) != 0 {
				t.Errorf("不应识别为 publish_good：%q，得到 %+v", txt, res.Actions)
			}
		})
	}
}

func TestRegex_HardReject_CancelKeywords(t *testing.T) {
	// hard rule 14：撤回 / 改主意整条 input 直接 drop
	rejects := []string{
		"出三层鞋架 6元 算了",
		"算了不卖了",
		"刚才那个不算 出鞋架 6元",
		"忽略我刚才说的",
	}
	for _, txt := range rejects {
		t.Run(txt, func(t *testing.T) {
			res := RecognizeViaRegex(singleMsg(txt))
			if len(res.Actions) != 0 {
				t.Errorf("hard reject 应当 drop：%q，得到 %+v", txt, res.Actions)
			}
		})
	}
}

func TestRegex_OffShelf_PlainAndWithHint(t *testing.T) {
	cases := []struct {
		text     string
		wantHint string
	}{
		{"已出", ""},
		{"已找到", ""},
		{"出掉了", ""},
		{"鞋架已出", "鞋架"},
		{"三层鞋架已出", "三层鞋架"},
		{"已出鞋架", "鞋架"},
		{"已找到 题库", "题库"},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			res := RecognizeViaRegex(singleMsg(c.text))
			if len(res.Actions) != 1 {
				t.Fatalf("应识别为 1 条 off_shelf，得到 %d: %+v", len(res.Actions), res.Actions)
			}
			a := res.Actions[0]
			if a.Type != "off_shelf" {
				t.Errorf("type = %q, want off_shelf", a.Type)
			}
			if a.OffShelfHint != c.wantHint {
				t.Errorf("hint = %q, want %q", a.OffShelfHint, c.wantHint)
			}
		})
	}
}

func TestRegex_OffShelf_RejectNoise(t *testing.T) {
	rejects := []string{
		"快乐出门去玩了",
		"今天我家狗出门没带伞",
		"我已经决定不卖了", // 含"不卖了" 命中 hard reject
	}
	for _, txt := range rejects {
		t.Run(txt, func(t *testing.T) {
			res := RecognizeViaRegex(singleMsg(txt))
			if len(res.Actions) != 0 {
				t.Errorf("不应识别：%q，得到 %+v", txt, res.Actions)
			}
		})
	}
}

func TestRegex_IgnoreImageOnlyMessage(t *testing.T) {
	// 纯 [图片] 消息 → drop（满足 hard rule 13）
	in := RecognizeInput{
		Messages: []RecognizeMsg{
			{MessageID: 1, Time: "12:00:00", Segments: []string{"[图片]"}},
		},
	}
	res := RecognizeViaRegex(in)
	if len(res.Actions) != 0 {
		t.Errorf("纯图片消息不应识别，得到 %+v", res.Actions)
	}
}

func TestRegex_PriceUpperLimit(t *testing.T) {
	// 防误识别：单价超过上限的"商品"应当不命中
	res := RecognizeViaRegex(singleMsg("出二手别墅 5000000元"))
	if len(res.Actions) != 0 {
		t.Errorf("超出价格上限应当 drop，得到 %+v", res.Actions)
	}
}

func TestRegex_MultiMessageAggregation(t *testing.T) {
	// 多条消息 → 多个独立 action（窗口里有两条独立的上架）
	in := RecognizeInput{
		Messages: []RecognizeMsg{
			{MessageID: 1, Time: "12:00:00", Segments: []string{"出三层鞋架 6元"}},
			{MessageID: 2, Time: "12:00:30", Segments: []string{"[图片]"}},
			{MessageID: 3, Time: "12:01:00", Segments: []string{"卖自行车 200元"}},
		},
	}
	res := RecognizeViaRegex(in)
	if len(res.Actions) != 2 {
		t.Fatalf("应识别为 2 条 action，得到 %d: %+v", len(res.Actions), res.Actions)
	}
	if res.Actions[0].Title != "三层鞋架" || res.Actions[1].Title != "自行车" {
		t.Errorf("两条 action 顺序应当跟消息顺序一致；得到 %v / %v",
			res.Actions[0].Title, res.Actions[1].Title)
	}
	if res.Actions[0].SourceMessageIDs[0] != 1 || res.Actions[1].SourceMessageIDs[0] != 3 {
		t.Errorf("source_message_ids 应跟消息 ID 一一对应")
	}
}
