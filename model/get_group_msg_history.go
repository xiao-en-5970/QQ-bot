package model

import "qq_bot/conf"

type GetGroupMsgHistoryReq struct {
	BaseReq
	MessageSeq int64 `json:"message_seq,omitempty"`
	GroupID    int64 `json:"group_id"`
}

type GetGroupMsgHistoryData struct {
	Messages []Message `json:"messages"`
}

type GetGroupMsgHistoryResp struct {
	BaseResp
	Data GetGroupMsgHistoryData `json:"data"`
}

type GetGroupMsgHistory struct {
	Req  *GetGroupMsgHistoryReq
	Resp *GetGroupMsgHistoryResp
}

func (g GetGroupMsgHistory) Name() string {
	return conf.Cfg.Server.Address + "get_group_msg_history"
}

func (g GetGroupMsgHistory) GetReq() interface{} {
	return g.Req
}

func (g GetGroupMsgHistory) GetResp() interface{} {
	return g.Resp
}
