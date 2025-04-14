package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

func SendGroupImg(client *http.Client, group_id int64, userID int64, text string) (err error) {

	err, _ = service.SendGroupMsg(client, &model.SendGroupMsgReq{
		GroupID: group_id,
		Message: []model.MessageContent{
			{
				Type: "at",
				Data: model.AtData{
					QQ:   userID,
					Name: "",
				},
			},
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
