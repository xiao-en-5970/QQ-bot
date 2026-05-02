package model

import "qq_bot/conf"

// NapCat：获取群列表
//   POST {server}/get_group_list
//   doc:  napcat.apifox.cn -> 群组接口 -> 获取群列表
//
// 请求 body：{"no_cache": bool|string}（可选，默认 false）
// 响应 data：OB11Group[]

type GetGroupListReq struct {
	BaseReq
	NoCache bool `json:"no_cache,omitempty"`
}

// OB11Group 字段参考 NapCat apifox -> 数据模型 -> OB11Group。
type OB11Group struct {
	GroupID         int64  `json:"group_id"`
	GroupName       string `json:"group_name"`
	GroupRemark     string `json:"group_remark,omitempty"`
	GroupMemo       string `json:"group_memo,omitempty"`
	GroupCreateTime uint32 `json:"group_create_time,omitempty"`
	GroupLevel      uint32 `json:"group_level,omitempty"`
	MemberCount     int32  `json:"member_count"`
	MaxMemberCount  int32  `json:"max_member_count"`
}

type GetGroupListResp struct {
	BaseResp
	Data []OB11Group `json:"data"`
}

func (r *GetGroupListResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type GetGroupList struct {
	Req  *GetGroupListReq
	Resp *GetGroupListResp
}

func (g GetGroupList) Name() string {
	return conf.Cfg.Server.Address + "get_group_list"
}

func (g GetGroupList) GetReq() interface{}  { return g.Req }
func (g GetGroupList) GetResp() interface{} { return g.Resp }
