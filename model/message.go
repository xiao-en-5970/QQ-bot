package model

import (
	"encoding/json"
)

// Data 定义 data 字段的结构

// Message 定义每条消息的结构
type Message struct {
	SelfID        int64            `json:"self_id"`
	UserID        int64            `json:"user_id"`
	Time          int64            `json:"time"`
	MessageID     int64            `json:"message_id"`
	MessageSeq    int64            `json:"message_seq"`
	MessageType   string           `json:"message_type"`
	Sender        Sender           `json:"sender"`
	RawMessage    string           `json:"raw_message"`
	Font          int              `json:"font"`
	SubType       string           `json:"sub_type"`
	Message       []MessageContent `json:"message"`
	MessageFormat string           `json:"message_format"`
	PostType      string           `json:"post_type"`
	GroupID       int64            `json:"group_id"`
}

// Sender 定义消息发送者的结构
type Sender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
	Role     string `json:"role"`
	Title    string `json:"title"`
}

// MessageContent 定义消息内容的接口
type MessageContent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type TextData struct {
	Text string `json:"text"`
}

type FaceData struct {
	ID int `json:"id"`
}

type AtData struct {
	QQ   int    `json:"qq"`
	Name string `json:"name"`
}

type ImageData struct {
	File     string `json:"file"`
	SubType  int    `json:"subType"`
	URL      string `json:"url"`
	FileSize string `json:"file_size"`
}

type MFaceData struct {
	Summary        string `json:"summary"`
	URL            string `json:"url"`
	EmojiID        string `json:"emoji_id"`
	EmojiPackageID string `json:"emoji_package_id"`
	Key            string `json:"key"`
}

type ForwardData struct {
	ID int64 `json:"id"`
}

func ParseMessageContent(mc MessageContent) interface{} {
	switch mc.Type {
	case "text":
		var data TextData
		json.Unmarshal([]byte(mc.Data.(string)), &data)
		return data
	case "face":
		var data FaceData
		json.Unmarshal([]byte(mc.Data.(string)), &data)
		return data
	case "image":
		var data ImageData
		json.Unmarshal([]byte(mc.Data.(string)), &data)
		return data
	case "mface":
		var data MFaceData
		json.Unmarshal([]byte(mc.Data.(string)), &data)
		return data
	case "forward":
		var data ForwardData
		json.Unmarshal([]byte(mc.Data.(string)), &data)
		return data
	default:
		return nil
	}
}
