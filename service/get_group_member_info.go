package service

import (
	"net/http"
	"qq_bot/model"
)

// GetGroupMemberInfo 调 NapCat 的 get_group_member_info 拿成员 role。
//
// 调用约束：
//   - bot 必须在 group 里（否则 NapCat 返业务错）
//   - target user 必须在 group 里（否则同样业务错）
//   - 这两类错走 error 路径返回；调用方应当兜底为"未验证"，不要让群接入申请因此中断
func GetGroupMemberInfo(client *http.Client, req *model.GetGroupMemberInfoReq) (err error, resp *model.GetGroupMemberInfoResp) {
	resp = new(model.GetGroupMemberInfoResp)
	return BaseService(client, model.GetGroupMemberInfo{Req: req, Resp: resp}), resp
}
