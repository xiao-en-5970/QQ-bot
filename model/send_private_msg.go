package model

import "qq_bot/conf"

// NapCat：发送私聊消息
//
//	POST {server}/send_private_msg
//	doc:  napcat.apifox.cn -> 个人接口 -> 发送私聊消息
//
// NapCat 兼容 OneBot11，message 既可以是 string 也可以是 OB11MessageData[]，
// 跟 send_group_msg 同款；这里统一走 array 形式以便组合多段（虽然 P2a 实际只发文字）。
//
// 调用前提：
//   - bot QQ 跟接收方 QQ 必须是好友（或同一群里、对方允许临时会话）
//   - 用于 QQ 绑定流程时，前置 logic.CheckFriend 已确认好友关系
//
// retcode 100 = "对方不是好友"——上层应该让用户先去加 bot 为好友再继续。

type SendPrivateMsgReq struct {
	BaseReq
	UserID     int64            `json:"user_id"`
	Message    []MessageSegment `json:"message"`
	AutoEscape bool             `json:"auto_escape,omitempty"`
}

type SendPrivateMsgData struct {
	MessageID int64 `json:"message_id"`
}

type SendPrivateMsgResp struct {
	BaseResp
	Data SendPrivateMsgData `json:"data"`
}

func (r *SendPrivateMsgResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type SendPrivateMsg struct {
	Req  *SendPrivateMsgReq
	Resp *SendPrivateMsgResp
}

func (s SendPrivateMsg) Name() string {
	return conf.Cfg.Server.Address + "send_private_msg"
}

func (s SendPrivateMsg) GetReq() interface{}  { return s.Req }
func (s SendPrivateMsg) GetResp() interface{} { return s.Resp }
