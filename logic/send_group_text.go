package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

func SendGroupText(client *http.Client, group_id int64, text string) (err error) {

	err, _ = service.SendGroupMsg(client, &model.SendGroupMsgReq{
		GroupID: group_id,
		Message: []model.MessageContent{
			{
				Type: "text",
				Data: model.TextData{
					Text: " " + text,
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
