package service

import (
	"net/http"
	"qq_bot/model"
)

// GetFriendList 调 NapCat 拿 bot 当前的好友列表。
//
// noCache=true 强制刷新（NapCat 内部对好友列表做了缓存）；
// 绑定流程默认 noCache=false 即可——刚加好友的用户也要等几秒同步，
// 但 5min 验证码 TTL 足够覆盖。
func GetFriendList(client *http.Client, noCache bool) (err error, resp *model.GetFriendListResp) {
	resp = new(model.GetFriendListResp)
	return BaseService(client, model.GetFriendList{
		Req:  &model.GetFriendListReq{NoCache: noCache},
		Resp: resp,
	}), resp
}
