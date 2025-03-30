package service

import (
	"net/http"
	"qq_bot/model"
)

func GetGroupList(client *http.Client, messageReq *model.GetGroupListReq) (err error, messageResp *model.GetGroupListResp) {
	messageResp = new(model.GetGroupListResp)
	return BaseService(client, model.GetGroupList{Req: messageReq, Resp: messageResp}), messageResp
}
