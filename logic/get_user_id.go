package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// GetUserId 调 NapCat /get_login_info 拿当前 bot 自身的 QQ 号。
func GetUserId(client *http.Client) (err error, userID int64) {
	err, resp := service.GetLoginInfo(client, &model.GetLoginInfoReq{})
	if err != nil {
		zaplog.Logger.Errorf("get_login_info failed: %v", err)
		return err, -1
	}
	return nil, resp.Data.UserID
}
