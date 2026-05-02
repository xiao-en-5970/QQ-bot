package model

import (
	"encoding/json"
	"fmt"
)

// Message 对应 NapCat 的 OB11Message。
//
// NapCat 默认 message_format = "array"，所以 message 字段是 OB11MessageData[]。
// 这里保留我们项目中实际用到的字段，参考 NapCat apifox -> 数据模型 -> OB11Message。
type Message struct {
	SelfID        int64            `json:"self_id"`
	UserID        int64            `json:"user_id"`
	Time          int64            `json:"time"`
	MessageID     int64            `json:"message_id"`
	MessageSeq    int64            `json:"message_seq"`
	RealID        int64            `json:"real_id,omitempty"`
	MessageType   string           `json:"message_type"`
	Sender        Sender           `json:"sender"`
	RawMessage    string           `json:"raw_message"`
	Font          int              `json:"font"`
	SubType       string           `json:"sub_type"`
	Message       []MessageSegment `json:"message"`
	MessageFormat string           `json:"message_format"`
	PostType      string           `json:"post_type"`
	GroupID       int64            `json:"group_id"`
}

// Sender 对应 NapCat 的 OB11Sender。
type Sender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card,omitempty"`
	Role     string `json:"role,omitempty"`
	Title    string `json:"title,omitempty"`
}

// MessageSegment 是 OneBot11 数组型 message 的单个段。
//
// NapCat 文档：OB11MessageData = OB11MessageText | OB11MessageAt | OB11MessageImage | ...
// 反序列化后 Data 一般是 map[string]interface{}，发送时也允许传 struct / map。
type MessageSegment struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// ---------------------------------------------------------------------------
// NapCat 文档里常见的 segment data 结构
// ---------------------------------------------------------------------------

// TextData 对应 OB11MessageText.data
type TextData struct {
	Text string `json:"text"`
}

// AtData 对应 OB11MessageAt.data
//
// qq 字段 NapCat 文档里规定为 string（"123" 或 "all"），实际可能下发 number。
// 发送时统一发字符串以最大化兼容性。
type AtData struct {
	QQ   string `json:"qq"`
	Name string `json:"name,omitempty"`
}

// ImageData 对应 OB11MessageImage.data（继承 OB11MessageFileBase）
//
// 字段说明：
//   File     本地路径 / URL / file:// / base64://
//   SubType  0 普通图片，1 表情
//   Summary  外显文本
type ImageData struct {
	File     string `json:"file"`
	Name     string `json:"name,omitempty"`
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	Thumb    string `json:"thumb,omitempty"`
	SubType  int    `json:"sub_type,omitempty"`
	Summary  string `json:"summary,omitempty"`
	FileSize string `json:"file_size,omitempty"`
}

// MFaceData 对应 OB11MessageMFace.data
type MFaceData struct {
	Summary        string `json:"summary,omitempty"`
	URL            string `json:"url,omitempty"`
	EmojiID        string `json:"emoji_id,omitempty"`
	EmojiPackageID string `json:"emoji_package_id,omitempty"`
	Key            string `json:"key,omitempty"`
}

// FaceData 对应 OB11MessageFace.data
type FaceData struct {
	ID int `json:"id"`
}

// ReplyData 对应 OB11MessageReply.data
type ReplyData struct {
	ID string `json:"id"`
}

// ForwardData 对应 OB11MessageForward.data
type ForwardData struct {
	ID string `json:"id"`
}

// ---------------------------------------------------------------------------
// segment.Data 解析帮助函数
//
// Go 标准库反序列化时，interface{} 会变成 map[string]interface{}（JSON object）
// 或 []interface{}（数组）；为了避免上游忘了类型断言，统一走 marshal -> unmarshal。
// ---------------------------------------------------------------------------

// AsTextData 把 segment.Data 当作 OB11MessageText.data 解析。
func AsTextData(data interface{}) (TextData, error) {
	var out TextData
	if err := decodeSegment(data, &out); err != nil {
		return TextData{}, err
	}
	return out, nil
}

// AsAtData 把 segment.Data 当作 OB11MessageAt.data 解析。
//
// qq 字段允许 string / number / json.Number 三种来源。
func AsAtData(data interface{}) (AtData, error) {
	if m, ok := data.(map[string]interface{}); ok {
		out := AtData{}
		switch v := m["qq"].(type) {
		case string:
			out.QQ = v
		case float64:
			out.QQ = fmt.Sprintf("%d", int64(v))
		case int:
			out.QQ = fmt.Sprintf("%d", v)
		case int64:
			out.QQ = fmt.Sprintf("%d", v)
		case json.Number:
			out.QQ = v.String()
		}
		if name, ok := m["name"].(string); ok {
			out.Name = name
		}
		return out, nil
	}
	var out AtData
	err := decodeSegment(data, &out)
	return out, err
}

// AsImageData 把 segment.Data 当作 OB11MessageImage.data 解析。
//
// 兼容旧 LLOneBot 的 camelCase 字段 subType。
func AsImageData(data interface{}) (ImageData, error) {
	var out ImageData
	if err := decodeSegment(data, &out); err != nil {
		return ImageData{}, err
	}
	if m, ok := data.(map[string]interface{}); ok && out.SubType == 0 {
		if v, ok := m["subType"]; ok {
			if x, ok := v.(float64); ok {
				out.SubType = int(x)
			}
		}
	}
	return out, nil
}

// decodeSegment 把任意 segment.Data 解码到指定结构体。
func decodeSegment(data interface{}, out interface{}) error {
	if data == nil {
		return nil
	}
	if s, ok := data.(string); ok {
		// 上游有可能直接给原始 JSON 字符串
		return json.Unmarshal([]byte(s), out)
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
