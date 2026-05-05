package logic

import (
	"net/http"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// CheckFriend 判断指定 QQ 号是不是 bot 的好友。
//
// 用途：QQ 绑定流程中 hfut 调本接口确认"目标 QQ 已加 bot 好友"——不是的话发不出私聊
// 验证码，应该让用户先去添加 bot 为好友。
//
// 实现：拉一次 NapCat 的 friend list（NapCat 内部缓存，调用很快）后线性查找。
//
// 失败语义：
//   - NapCat 整体不可达 / 业务错 → 返 (false, err)，让上层拒绝绑定流程并提示"系统繁忙"
//   - 调用成功但列表里找不到 → 返 (false, nil)
//
// noCache=true：强制刷新好友列表；用户刚加 bot 为好友的几秒内 NapCat 缓存还没更新，
// 上层（特别是 hfut 那边的 RequestBindCode）可以传 true 让校验更准。
func CheckFriend(client *http.Client, qq int64, noCache bool) (bool, error) {
	err, resp := service.GetFriendList(client, noCache)
	if err != nil {
		zaplog.Logger.Errorf("check_friend get_friend_list 失败 qq=%d: %v", qq, err)
		return false, err
	}
	for _, f := range resp.Data {
		if f.UserID == qq {
			return true, nil
		}
	}
	return false, nil
}
