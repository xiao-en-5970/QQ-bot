package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// SendGroupText 向群里发送一条纯文本消息（NapCat OB11MessageText）。
func SendGroupText(client *http.Client, groupID int64, text string) error {
	err, _ := service.SendGroupMsg(client, &model.SendGroupMsgReq{
		GroupID: groupID,
		Message: []model.MessageSegment{
			{
				Type: "text",
				Data: model.TextData{Text: text},
			},
		},
	})
	if err != nil {
		zaplog.Logger.Errorf("napcat send_group_msg(text) failed group=%d: %v", groupID, err)
		return err
	}
	return nil
}
