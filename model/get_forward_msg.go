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

type ForwardNodeData struct {
	UserID   int64            `json:"user_id"`
	Nickname string           `json:"nickname"`
	Time     int64            `json:"time"`
	Sender   *Sender          `json:"sender,omitempty"`
	Message  []MessageSegment `json:"message"`
	// Content 是 NapCat 旧版字段，等价 Message；reflect 解码后由 helper 兜底。
	Content []MessageSegment `json:"content,omitempty"`
}

type ForwardNode struct {
	Type string          `json:"type"`
	Data ForwardNodeData `json:"data"`
}

// EffectiveSegments 返回 node 真正的 segments：优先 Message，回退 Content。
func (n *ForwardNode) EffectiveSegments() []MessageSegment {
	if n == nil {
		return nil
	}
	if len(n.Data.Message) > 0 {
		return n.Data.Message
	}
	return n.Data.Content
}

// EffectiveUserID 返回 node 真正的发送者 user_id：
// 优先 data.user_id，回退 data.sender.user_id。
func (n *ForwardNode) EffectiveUserID() int64 {
	if n == nil {
		return 0
	}
	if n.Data.UserID != 0 {
		return n.Data.UserID
	}
	if n.Data.Sender != nil {
		return n.Data.Sender.UserID
	}
	return 0
}

// EffectiveNickname 同上。
func (n *ForwardNode) EffectiveNickname() string {
	if n == nil {
		return ""
	}
	if n.Data.Nickname != "" {
		return n.Data.Nickname
	}
	if n.Data.Sender != nil {
		return n.Data.Sender.Nickname
	}
	return ""
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
