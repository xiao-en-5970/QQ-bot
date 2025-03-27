package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

func GetUserId(client *http.Client) (err error, userID int64) {
	err, resp := service.GetLoginInfo(client, &model.GetLoginInfoReq{})

	if err != nil {
		zaplog.Logger.Fatalf("Msg send failed: %v", err)
		return err, -1
	}
	return nil, resp.GetLoginInfoData.UserId
}
