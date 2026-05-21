package service

import (
	"net/http"
	"qq_bot/model"
)

// GetForwardMsg 调 NapCat /get_forward_msg 拿合并转发消息的子消息列表。
//
// 调用方需要拿到外层 forward segment 的 id（model.AsForwardData(seg.Data).ID）后传进来。
// 失败时 resp.Data.Messages 为空，由调用方决定是否做"占位符兜底"。
func GetForwardMsg(client *http.Client, req *model.GetForwardMsgReq) (error, *model.GetForwardMsgResp) {
	resp := new(model.GetForwardMsgResp)
	return BaseService(client, model.GetForwardMsg{Req: req, Resp: resp}), resp
}
