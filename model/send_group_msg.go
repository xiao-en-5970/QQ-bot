package model

type SendGroupMsgReq struct {
	BaseReq
	GroupID    int64            `json:"group_id"`
	Message    []MessageContent `json:"message"`
	AutoEscape bool             `json:"auto_escape"`
}

type SendGroupMsgData struct {
	MessageID int64 `json:"message_id"`
}

type SendGroupMsgResp struct {
	BaseResp
	Data SendGroupMsgData `json:"data"`
}

type SendGroupMsg struct {
	Req  *SendGroupMsgReq
	Resp *SendGroupMsgResp
}

func (g SendGroupMsg) Name() string {
	return "send_group_msg"
}

func (g SendGroupMsg) GetReq() interface{} {
	return g.Req
}

func (g SendGroupMsg) GetResp() interface{} {
	return g.Resp
}
