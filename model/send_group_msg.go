package model

import "qq_bot/conf"

// NapCat：发送群消息
//   POST {server}/send_group_msg
//   doc:  napcat.apifox.cn -> 群组接口 -> 发送群消息
//
// NapCat 兼容 OneBot11，message 既可以是 string 也可以是 OB11MessageData[]，
// 这里走 array 形式以便组合 at + text + image 等多个段。

type SendGroupMsgReq struct {
	BaseReq
	GroupID    int64            `json:"group_id"`
	Message    []MessageSegment `json:"message"`
	AutoEscape bool             `json:"auto_escape,omitempty"`
}

type SendGroupMsgData struct {
	MessageID int64 `json:"message_id"`
}

type SendGroupMsgResp struct {
	BaseResp
	Data SendGroupMsgData `json:"data"`
}

func (r *SendGroupMsgResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type SendGroupMsg struct {
	Req  *SendGroupMsgReq
	Resp *SendGroupMsgResp
}

func (g SendGroupMsg) Name() string {
	return conf.Cfg.Server.Address + "send_group_msg"
}

func (g SendGroupMsg) GetReq() interface{}  { return g.Req }
func (g SendGroupMsg) GetResp() interface{} { return g.Resp }
