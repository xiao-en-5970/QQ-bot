package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// SendGroupText 向群里发送一条纯文本消息（NapCat OB11MessageText）。
//
// 灰度静默 (SilentMode) 开启 + 目标群非运维群时直接返回 nil，不发 NapCat。
// 详见 logic/silent_gate.go。
func SendGroupText(client *http.Client, groupID int64, text string) error {
	if silentSuppressGroup(groupID, text) {
		return nil
	}
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
