package model

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
