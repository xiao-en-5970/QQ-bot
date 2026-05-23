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

func TestHasMeaningfulText(t *testing.T) {
	if hasMeaningfulText(imageMsg(1)) {
		t.Error("纯图消息不应算业务文字")
	}
	if !hasMeaningfulText(textMsg(2, "出鞋架")) {
		t.Error("纯文字消息应算业务文字")
	}
	if hasMeaningfulText(textMsg(3, "   ")) {
		t.Error("只有空白字符的文字不应算业务文字")
	}
}

func TestBucketHasMeaningfulText(t *testing.T) {
	if bucketHasMeaningfulText(nil) {
		t.Error("空桶不应有业务文字")
	}
	if bucketHasMeaningfulText([]autoReplyMsg{imageMsg(1), imageMsg(2)}) {
		t.Error("纯图桶不应有业务文字")
	}
	if !bucketHasMeaningfulText([]autoReplyMsg{imageMsg(1), textMsg(2, "x")}) {
		t.Error("含文字的桶应有业务文字")
	}
}
