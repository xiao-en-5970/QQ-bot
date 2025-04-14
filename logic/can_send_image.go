package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

func CanSendImage(client *http.Client) (err error, yes bool) {
	err, resp := service.CanSendImage(client, &model.CanSendImageReq{})

	if err != nil {
		zaplog.Logger.Fatalf("CanSendImage failed: %v", err)
		return err, false
	}
	return nil, resp.CanSendImageData.Yes
}
