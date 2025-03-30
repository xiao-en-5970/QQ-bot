package service

import (
	"net/http"
	"qq_bot/model"
)

func GetGroupMsgHistory(client *http.Client, messageReq *model.GetGroupMsgHistoryReq) (err error, messageResp *model.GetGroupMsgHistoryResp) {
	messageResp = new(model.GetGroupMsgHistoryResp)
	return BaseService(client, model.GetGroupMsgHistory{Req: messageReq, Resp: messageResp}), messageResp
}
