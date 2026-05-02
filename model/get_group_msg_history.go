package model

import "qq_bot/conf"

// NapCat：获取群历史消息
//   POST {server}/get_group_msg_history
//   doc:  napcat.apifox.cn -> 消息相关 -> 获取群历史消息
//
// NapCat 文档里 group_id / message_seq 都允许 number | string。
// Count 默认 20，倒序由 ReverseOrder 控制。

type GetGroupMsgHistoryReq struct {
	BaseReq
	GroupID      int64 `json:"group_id"`
	MessageSeq   int64 `json:"message_seq,omitempty"`
	Count        int64 `json:"count,omitempty"`
	ReverseOrder bool  `json:"reverseOrder,omitempty"`
}

type GetGroupMsgHistoryData struct {
	Messages []Message `json:"messages"`
}

type GetGroupMsgHistoryResp struct {
	BaseResp
	Data GetGroupMsgHistoryData `json:"data"`
}

func (r *GetGroupMsgHistoryResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type GetGroupMsgHistory struct {
	Req  *GetGroupMsgHistoryReq
	Resp *GetGroupMsgHistoryResp
}

func (g GetGroupMsgHistory) Name() string {
	return conf.Cfg.Server.Address + "get_group_msg_history"
}

func (g GetGroupMsgHistory) GetReq() interface{}  { return g.Req }
func (g GetGroupMsgHistory) GetResp() interface{} { return g.Resp }
