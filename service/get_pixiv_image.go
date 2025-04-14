package service

import (
	"net/http"
	url1 "net/url"
	"qq_bot/model"
)

func GetPixivImage(client *http.Client, messageReq *model.GetPixivImageReq, form url1.Values) (err error, messageResp *model.GetPixivImageResp) {
	messageResp = new(model.GetPixivImageResp)
	return BaseServiceWithForm(client, model.GetPixivImage{Req: messageReq, Resp: messageResp}, form), messageResp
}
