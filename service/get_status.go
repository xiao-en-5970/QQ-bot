package service

import (
	"net/http"
	"qq_bot/model"
)

// GetStatus 调用 NapCat /get_status，用于启动期连通性检查。
func GetStatus(client *http.Client, messageReq *model.GetStatusReq) (err error, messageResp *model.GetStatusResp) {
	messageResp = new(model.GetStatusResp)
	return BaseService(client, model.GetStatus{Req: messageReq, Resp: messageResp}), messageResp
}
