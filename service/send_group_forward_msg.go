package service

import (
	"net/http"
	"qq_bot/model"
)

// SendGroupForwardMsg 调 NapCat /send_group_forward_msg 发"合并转发"到群。
//
// 一次性发送，不需要 bot 先发给自己再多选转发——构造好 messages 数组（自定义节点）
// 直接 POST 即可。详见 model.SendGroupForwardMsg 注释。
func SendGroupForwardMsg(client *http.Client, req *model.SendGroupForwardMsgReq) (error, *model.SendGroupForwardMsgResp) {
	resp := new(model.SendGroupForwardMsgResp)
	return BaseService(client, model.SendGroupForwardMsg{Req: req, Resp: resp}), resp
}
