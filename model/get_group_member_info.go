package model

import "qq_bot/conf"

// NapCat：获取群成员信息
//
//	POST {server}/get_group_member_info
//	doc:  napcat.apifox.cn -> 群组接口 -> 获取群成员信息
//
// 用途：判断某 QQ 在某群里是否为群主 / 管理员（或就是普通成员、或不在群里）。
// 群接入申请流程会用 role 字段验证申请人确实有该群管理权。
//
// 错误处理：
//   - bot 自己不在 target group → NapCat 通常返回 retcode != 0 + message 含
//     "群不存在" / "未加入该群"，BaseService 会包成 error 返回；上层应当兜底为
//     "未验证"（但不要因此终止流程）。
//   - 该 QQ 不在群里 → 同样会失败，上层一并兜底
//   - no_cache=true 强制刷新（群权限可能刚变动）

type GetGroupMemberInfoReq struct {
	BaseReq
	GroupID int64 `json:"group_id"`
	UserID  int64 `json:"user_id"`
	NoCache bool  `json:"no_cache,omitempty"`
}

// OB11GroupMember 字段参考 NapCat apifox -> 数据模型 -> OB11GroupMember。
//
// Role 取值（OneBot11 标准）：
//
//	"owner"  群主
//	"admin"  管理员
//	"member" 普通成员
type OB11GroupMember struct {
	GroupID  int64  `json:"group_id"`
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card,omitempty"`
	Role     string `json:"role"`
}

type GetGroupMemberInfoResp struct {
	BaseResp
	Data OB11GroupMember `json:"data"`
}

func (r *GetGroupMemberInfoResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type GetGroupMemberInfo struct {
	Req  *GetGroupMemberInfoReq
	Resp *GetGroupMemberInfoResp
}

func (g GetGroupMemberInfo) Name() string {
	return conf.Cfg.Server.Address + "get_group_member_info"
}

func (g GetGroupMemberInfo) GetReq() interface{}  { return g.Req }
func (g GetGroupMemberInfo) GetResp() interface{} { return g.Resp }
