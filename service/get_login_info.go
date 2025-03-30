package service

import (
	"net/http"
	"qq_bot/model"
)

func GetLoginInfo(client *http.Client, messageReq *model.GetLoginInfoReq) (err error, messageResp *model.GetLoginInfoResp) {
	messageResp = new(model.GetLoginInfoResp)
	return BaseService(client, model.GetLoginInfo{Req: messageReq, Resp: messageResp}), messageResp
}
