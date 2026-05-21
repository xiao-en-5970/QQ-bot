package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
	"strconv"
)

// SendGroupAtText 发送一条「@某人 + 文本」的群消息。
//
// 对应 NapCat 的 OB11MessageAt + OB11MessageText 组合，
// 其中 at.qq 走字符串以兼容文档要求。
//
// 灰度静默 (SilentMode) 开启 + 目标群非运维群时直接返回 nil，不发 NapCat。
// 详见 logic/silent_gate.go。
func SendGroupAtText(client *http.Client, groupID int64, userID int64, text string) error {
	if silentSuppressGroup(groupID, text) {
		return nil
	}
	err, _ := service.SendGroupMsg(client, &model.SendGroupMsgReq{
		GroupID: groupID,
		Message: []model.MessageSegment{
			{
				Type: "at",
				Data: model.AtData{QQ: strconv.FormatInt(userID, 10)},
			},
			{
				Type: "text",
				Data: model.TextData{Text: " " + text},
			},
		},
	})
	if err != nil {
		zaplog.Logger.Errorf("napcat send_group_msg(at+text) failed group=%d user=%d: %v", groupID, userID, err)
		return err
	}
	return nil
}
