package model

import (
	"encoding/json"
	"fmt"
	"qq_bot/conf"
)

// NapCat：获取合并转发消息
//   POST {server}/get_forward_msg
//   doc: napcat.apifox.cn -> 消息接口 -> 获取合并转发消息
//
// 用途：
//   群里的"聊天记录"消息体只是一条 segment `{type:"forward", data:{id:"..."}}`，
//   真正的子消息列表需要单独调这个接口拿。NapCat 官方 body 字段名为 message_id
//   （也接受 id 兜底，部分旧版客户端用 id）。
//
// 响应 messages[i] 是一个 ForwardNode：
//   - type 固定 "node"
//   - data 里有 user_id / nickname / time / message[]（即又是一组 segments）
//
// 已知兼容点：
//   - NapCat <2.6.10 曾把 inner segments 放在 data.content 字段；2.6.10 修复为 data.message。
//     这里两个字段都解，谁有取谁。
//   - inner data 里可能还嵌一个 sender{user_id,nickname}（go-cqhttp 风格），所以
//     ForwardNodeData 同时声明顶层 UserID / Nickname 和 Sender 两套字段，由 helper
//     合并。
//   - 节点的 message 字段在极少数版本里是 CQ 码字符串，本项目按 NapCat 默认
//     message_format=array 处理；非 array 时 segments 为空，由调用方决定如何降级。

type GetForwardMsgReq struct {
	BaseReq
	MessageID string `json:"message_id"`
	ID        string `json:"id,omitempty"` // 兜底字段
}

// ForwardNodeData OB11 标准 node 段的嵌套 data 结构。
//
// 实测中 NapCat 把字段**扁平**放在 ForwardNode 顶层（不嵌 data），所以本字段在实际
// 响应里通常是空——保留是为了向后兼容 OB11 标准格式（自定义 node 时仍会用嵌套结构）。
type ForwardNodeData struct {
	UserID   int64            `json:"user_id"`
	Nickname string           `json:"nickname"`
	Time     int64            `json:"time"`
	Sender   *Sender          `json:"sender,omitempty"`
	Message  []MessageSegment `json:"message"`
	Content  []MessageSegment `json:"content,omitempty"`
}

// ForwardNode 合并转发里的一条子消息。
//
// 兼容两种格式：
//
//   - **OB11 标准嵌套**（自定义节点常用）：{"type":"node","data":{"user_id":...,"message":[...]}}
//     → Type/Data 字段；EffectiveXxx 从 Data 取
//   - **NapCat 实测扁平**（/get_forward_msg 实际返回）：{"self_id":...,"user_id":...,
//     "sender":{...},"message":[...]}，所有字段直接在顶层
//     → FlatXxx 字段；EffectiveXxx 从顶层取
//
// EffectiveSegments / EffectiveUserID / EffectiveNickname 三个 helper 内部按顺序探测，
// 调用方不需要关心 NapCat 用了哪种格式。
type ForwardNode struct {
	// 嵌套格式字段（OB11 标准）
	Type string          `json:"type,omitempty"`
	Data ForwardNodeData `json:"data,omitempty"`

	// 扁平格式字段（NapCat 实测）—— 直接复用 Message 结构里同名 JSON tag
	FlatSelfID     int64            `json:"self_id,omitempty"`
	FlatUserID     int64            `json:"user_id,omitempty"`
	FlatTime       int64            `json:"time,omitempty"`
	FlatMessageID  int64            `json:"message_id,omitempty"`
	FlatSender     *Sender          `json:"sender,omitempty"`
	FlatMessage    []MessageSegment `json:"message,omitempty"`
	FlatContent    []MessageSegment `json:"content,omitempty"` // 旧版 NapCat 扁平格式下的 inner segments
	FlatRawMessage string           `json:"raw_message,omitempty"`
}

// EffectiveSegments 返回 node 真正的 segments：
// 优先嵌套 Data.Message → Data.Content → 扁平 FlatMessage → FlatContent。
func (n *ForwardNode) EffectiveSegments() []MessageSegment {
	if n == nil {
		return nil
	}
	if len(n.Data.Message) > 0 {
		return n.Data.Message
	}
	if len(n.Data.Content) > 0 {
		return n.Data.Content
	}
	if len(n.FlatMessage) > 0 {
		return n.FlatMessage
	}
	return n.FlatContent
}

// EffectiveUserID 同上探测顺序：Data.UserID → Data.Sender.UserID → FlatUserID →
// FlatSender.UserID。
func (n *ForwardNode) EffectiveUserID() int64 {
	if n == nil {
		return 0
	}
	if n.Data.UserID != 0 {
		return n.Data.UserID
	}
	if n.Data.Sender != nil && n.Data.Sender.UserID != 0 {
		return n.Data.Sender.UserID
	}
	if n.FlatUserID != 0 {
		return n.FlatUserID
	}
	if n.FlatSender != nil {
		return n.FlatSender.UserID
	}
	return 0
}

// EffectiveNickname 同 EffectiveUserID 探测顺序。
func (n *ForwardNode) EffectiveNickname() string {
	if n == nil {
		return ""
	}
	if n.Data.Nickname != "" {
		return n.Data.Nickname
	}
	if n.Data.Sender != nil && n.Data.Sender.Nickname != "" {
		return n.Data.Sender.Nickname
	}
	if n.FlatSender != nil && n.FlatSender.Nickname != "" {
		return n.FlatSender.Nickname
	}
	return ""
}

// EffectiveTime 优先嵌套 → 扁平。0 表示 node 没带时间。
func (n *ForwardNode) EffectiveTime() int64 {
	if n == nil {
		return 0
	}
	if n.Data.Time > 0 {
		return n.Data.Time
	}
	return n.FlatTime
}

type GetForwardMsgData struct {
	Messages []ForwardNode `json:"messages"`
}

type GetForwardMsgResp struct {
	BaseResp
	Data GetForwardMsgData `json:"data"`
}

func (r *GetForwardMsgResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type GetForwardMsg struct {
	Req  *GetForwardMsgReq
	Resp *GetForwardMsgResp
}

func (g GetForwardMsg) Name() string {
	return conf.Cfg.Server.Address + "get_forward_msg"
}

func (g GetForwardMsg) GetReq() interface{}  { return g.Req }
func (g GetForwardMsg) GetResp() interface{} { return g.Resp }

// AsForwardData 把 segment.Data 当作 OB11MessageForward.data 解析。
// NapCat 文档里 id 字段为 string；这里也接受 number / json.Number。
func AsForwardData(data interface{}) (ForwardData, error) {
	if m, ok := data.(map[string]interface{}); ok {
		out := ForwardData{}
		switch v := m["id"].(type) {
		case string:
			out.ID = v
		case float64:
			out.ID = fmt.Sprintf("%d", int64(v))
		case int:
			out.ID = fmt.Sprintf("%d", v)
		case int64:
			out.ID = fmt.Sprintf("%d", v)
		case json.Number:
			out.ID = v.String()
		}
		return out, nil
	}
	var out ForwardData
	err := decodeSegment(data, &out)
	return out, err
}
