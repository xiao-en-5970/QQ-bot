package service

import (
	"net/http"
	"qq_bot/model"
)

func SendGroupMsg(client *http.Client, messageReq *model.SendGroupMsgReq) (err error, messageResp *model.SendGroupMsgResp) {
	messageResp = new(model.SendGroupMsgResp)
	return BaseService(client, model.SendGroupMsg{Req: messageReq, Resp: messageResp}), messageResp
}
