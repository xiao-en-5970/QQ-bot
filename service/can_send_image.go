package service

import (
	"net/http"
	"qq_bot/model"
)

func CanSendImage(client *http.Client, messageReq *model.CanSendImageReq) (err error, messageResp *model.CanSendImageResp) {
	messageResp = new(model.CanSendImageResp)
	return BaseService(client, model.CanSendImage{Req: messageReq, Resp: messageResp}), messageResp
}
