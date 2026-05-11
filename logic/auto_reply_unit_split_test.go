package logic

import (
	"encoding/json"
	"testing"

	"qq_bot/model"
)

// 构造一条 text segment 消息
func textMsg(id int64, content string) autoReplyMsg {
	td, _ := json.Marshal(model.TextData{Text: content})
	var raw json.RawMessage = td
	return autoReplyMsg{
		MessageID: id,
		Segments: []model.MessageSegment{
			{Type: "text", Data: raw},
		},
		FlatText: content,
	}
}

// 构造一条 image segment 消息（业务文字判定为 false）
func imageMsg(id int64) autoReplyMsg {
	return autoReplyMsg{
		MessageID: id,
		Segments: []model.MessageSegment{
			{Type: "image", Data: json.RawMessage("{}")},
		},
		FlatText: "[图片]",
	}
}

// 构造一条"图文同条"消息——QQ 输入框可以在一条 message 里同时塞 text 和 image
func textWithImageMsg(id int64, content string) autoReplyMsg {
	td, _ := json.Marshal(model.TextData{Text: content})
	var raw json.RawMessage = td
	return autoReplyMsg{
		MessageID: id,
		Segments: []model.MessageSegment{
			{Type: "text", Data: raw},
			{Type: "image", Data: json.RawMessage("{}")},
		},
		FlatText: content + " [图片]",
	}
}

// summarizeUnits 把 unit 序列描述成 "[图]*文" / "文[图]*" / 纯图 形式，方便测试断言
func summarize(units [][]autoReplyMsg) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		s := ""
		for _, m := range u {
			if hasMeaningfulText(m) {
				s += "T"
			} else {
				s += "I"
			}
		}
		out = append(out, s)
	}
	return out
}

func summarizeOne(u []autoReplyMsg) string {
	s := ""
	for _, m := range u {
		if hasMeaningfulText(m) {
			s += "T"
		} else {
			s += "I"
		}
	}
	return s
}

func TestSplitBucketIntoUnits(t *testing.T) {
	tests := []struct {
		name           string
		input          []autoReplyMsg
		wantCompleted  []string
		wantTail       string
	}{
		{
			name:          "empty",
			input:         nil,
			wantCompleted: nil,
			wantTail:      "",
		},
		{
			name:          "pure_images_no_text",
			input:         []autoReplyMsg{imageMsg(1), imageMsg(2), imageMsg(3)},
			wantCompleted: nil,
			wantTail:      "III",
		},
		{
			name:          "single_text",
			input:         []autoReplyMsg{textMsg(1, "出鞋架6元")},
			wantCompleted: nil,
			wantTail:      "T",
		},
		{
			name:          "mode_A_images_then_text",
			input:         []autoReplyMsg{imageMsg(1), imageMsg(2), imageMsg(3), textMsg(4, "出鞋架6元")},
			wantCompleted: []string{"IIIT"},
			wantTail:      "",
		},
		{
			name:          "mode_A_then_orphan_images",
			input:         []autoReplyMsg{imageMsg(1), imageMsg(2), imageMsg(3), textMsg(4, "出鞋架6元"), imageMsg(5)},
			wantCompleted: []string{"IIIT"},
			wantTail:      "I",
		},
		{
			name:          "mode_B_text_then_images",
			input:         []autoReplyMsg{textMsg(1, "出鞋架6元"), imageMsg(2), imageMsg(3), imageMsg(4)},
			wantCompleted: nil,
			wantTail:      "TIII",
		},
		{
			name:          "mode_B_two_consecutive_units",
			input:         []autoReplyMsg{textMsg(1, "出鞋架6元"), imageMsg(2), imageMsg(3), imageMsg(4), textMsg(5, "出杯子3元")},
			wantCompleted: []string{"TIII"},
			wantTail:      "T",
		},
		{
			name:          "two_modes_mixed",
			input:         []autoReplyMsg{imageMsg(1), imageMsg(2), textMsg(3, "出鞋架"), imageMsg(4), textMsg(5, "出杯子")},
			wantCompleted: []string{"IIT", "IT"},
			wantTail:      "",
		},
		{
			name:          "two_consecutive_texts",
			input:         []autoReplyMsg{textMsg(1, "出鞋架"), textMsg(2, "出杯子")},
			wantCompleted: []string{"T"},
			wantTail:      "T",
		},
		{
			name:          "long_mixed_sequence",
			input:         []autoReplyMsg{imageMsg(1), imageMsg(2), textMsg(3, "出鞋架"), imageMsg(4), imageMsg(5), textMsg(6, "出杯子"), imageMsg(7)},
			wantCompleted: []string{"IIT", "IIT"},
			wantTail:      "I",
		},
		// 图文同条快速路径——单条消息内部已经有 text+image，立即闭合
		{
			name:          "inline_image_single_message",
			input:         []autoReplyMsg{textWithImageMsg(1, "出鞋架6元")},
			wantCompleted: []string{"T"},
			wantTail:      "",
		},
		// 图文同条**自包含**——前面孤图被丢弃（很可能是无关闲聊的图）
		{
			name:          "inline_image_after_pending_images_drops_orphans",
			input:         []autoReplyMsg{imageMsg(1), imageMsg(2), textWithImageMsg(3, "出鞋架6元")},
			wantCompleted: []string{"T"},
			wantTail:      "",
		},
		{
			name:          "inline_image_then_more_images",
			input:         []autoReplyMsg{textWithImageMsg(1, "出鞋架6元"), imageMsg(2), imageMsg(3)},
			wantCompleted: []string{"T"},
			wantTail:      "II",
		},
		{
			name:          "active_unit_followed_by_inline",
			input:         []autoReplyMsg{textMsg(1, "出鞋架"), textWithImageMsg(2, "出杯子3元")},
			wantCompleted: []string{"T", "T"},
			wantTail:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			completed, tail := splitBucketIntoUnits(tc.input)
			gotCompleted := summarize(completed)
			gotTail := summarizeOne(tail)
			if len(gotCompleted) != len(tc.wantCompleted) {
				t.Fatalf("completed len mismatch: got %v, want %v", gotCompleted, tc.wantCompleted)
			}
			for i, want := range tc.wantCompleted {
				if gotCompleted[i] != want {
					t.Errorf("completed[%d]: got %q, want %q", i, gotCompleted[i], want)
				}
			}
			if gotTail != tc.wantTail {
				t.Errorf("tail: got %q, want %q", gotTail, tc.wantTail)
			}
		})
	}
}

func TestBucketHasMeaningfulText(t *testing.T) {
	if bucketHasMeaningfulText(nil) {
		t.Error("empty bucket should not have meaningful text")
	}
	if bucketHasMeaningfulText([]autoReplyMsg{imageMsg(1), imageMsg(2)}) {
		t.Error("pure image bucket should not have meaningful text")
	}
	if !bucketHasMeaningfulText([]autoReplyMsg{imageMsg(1), textMsg(2, "x")}) {
		t.Error("bucket with text should have meaningful text")
	}
}
