package service

import (
	"net/http"

	"qq_bot/model"
)

// GetStrangerInfo 调 NapCat 的 get_stranger_info 拿 QQ 全局昵称。
//
// 用于"旗下号 nickname 定期同步" scheduler——遍历所有 qq_number 拉最新昵称写回 hfut。
//
// 错误场景：
//   - 目标 QQ 注销 / 不存在 → NapCat 返业务错；上层 fallback "保持原值"
//   - bot 没登录 → 整个 ws 都断了，调用方会先看到 ws 错
func GetStrangerInfo(client *http.Client, req *model.GetStrangerInfoReq) (err error, resp *model.GetStrangerInfoResp) {
	resp = new(model.GetStrangerInfoResp)
	return BaseService(client, model.GetStrangerInfo{Req: req, Resp: resp}), resp
}
