package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// CanSendImage 调 NapCat /can_send_image，pix 等命令在发图前先确认能力。
func CanSendImage(client *http.Client) (err error, yes bool) {
	err, resp := service.CanSendImage(client, &model.CanSendImageReq{})
	if err != nil {
		zaplog.Logger.Errorf("can_send_image failed: %v", err)
		return err, false
	}
	return nil, resp.Data.Yes
}
