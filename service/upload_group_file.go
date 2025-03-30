package service

import (
	"net/http"
	"qq_bot/model"
)

func UploadGroupFile(client *http.Client, messageReq *model.UploadGroupFileReq) (err error, messageResp *model.UploadGroupFileResp) {
	messageResp = new(model.UploadGroupFileResp)
	return BaseService(client, model.UploadGroupFile{Req: messageReq, Resp: messageResp}), messageResp
}
