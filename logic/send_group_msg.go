package logic

import (
	"net/http"
	"qq_bot/conf"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

func SendGroupMsg(client *http.Client, text string) (err error) {

	err, _ = service.SendGroupMsg(client, &model.SendGroupMsgReq{
		GroupID: *conf.Cfg.GroupID,
		Message: []model.MessageContent{
			{
				Type: "text",
				Data: model.TextData{
					Text: text,
				},
			},
		},
	})
	if err != nil {
		zaplog.Logger.Fatalf("Msg send failed: %v", err)
		return err
	}
	return nil
}
