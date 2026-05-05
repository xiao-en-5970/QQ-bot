package service

import (
	"net/http"
	"qq_bot/model"
)

// SendPrivateMsg 调 NapCat 的 send_private_msg 发私聊消息。
//
// retcode 区分：
//   - 0  成功
//   - 100  对方不是好友 / 不允许临时会话——上层应让用户先加 bot 为好友
func SendPrivateMsg(client *http.Client, req *model.SendPrivateMsgReq) (err error, resp *model.SendPrivateMsgResp) {
	resp = new(model.SendPrivateMsgResp)
	return BaseService(client, model.SendPrivateMsg{Req: req, Resp: resp}), resp
}
