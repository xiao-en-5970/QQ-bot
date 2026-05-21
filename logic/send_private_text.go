package logic

import (
	"fmt"
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// SendPrivateText 给指定 QQ 用户发一条纯文本私聊。
//
// 前提：调用方应当**先**用 CheckFriend 确认 bot 与目标 QQ 是好友——否则 NapCat
// 直接 retcode 100 失败，业务层无法发出消息（QQ 绑定验证码就到不了用户手里）。
//
// 返回 NapCat 给的 message_id（成功时）+ error（任何路径上的错误）。
//
// 灰度静默 (SilentMode) **不影响私聊**——QQ 绑定 / 解绑验证码、订单加急、群接入
// 申请回执等用户主动触发的链路需要保留。silentSuppressPrivate 仅作为未来精细化
// 控制的预留 hook，当前永远 return false。
func SendPrivateText(client *http.Client, qq int64, text string) (int64, error) {
	if text == "" {
		return 0, fmt.Errorf("send_private_text: text 不能为空")
	}
	if silentSuppressPrivate(qq, text) {
		return 0, nil
	}
	err, resp := service.SendPrivateMsg(client, &model.SendPrivateMsgReq{
		UserID: qq,
		Message: []model.MessageSegment{
			{
				Type: "text",
				Data: model.TextData{Text: text},
			},
		},
	})
	if err != nil {
		zaplog.Logger.Errorf("napcat send_private_msg 失败 qq=%d: %v", qq, err)
		return 0, err
	}
	return resp.Data.MessageID, nil
}
