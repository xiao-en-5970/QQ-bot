package model

import "qq_bot/conf"

type GetGroupListReq struct {
	BaseReq
	NoCache bool `json:"no_cache,omitempty"`
}

// GetGroupInfoData (GetGroupListData = []GetGroupInfoData)
type GetGroupInfoData struct {
	GroupID         int64  `json:"group_id"`
	GroupName       string `json:"group_name"`
	GroupMemo       string `json:"group_memo"`
	GroupCreateTime uint32 `json:"group_create_time"`
	GroupLevel      uint32 `json:"group_level"`
	MemberCount     int32  `json:"member_count"`
	MaxMemberCount  int32  `json:"max_member_count"`
}

type GetGroupListResp struct {
	BaseResp
	Data []GetGroupInfoData `json:"data"`
}

type GetGroupList struct {
	Req  *GetGroupListReq
	Resp *GetGroupListResp
}

func (g GetGroupList) Name() string {
	return conf.Cfg.Server.Address + "get_group_list"
}

func (g GetGroupList) GetReq() interface{} {
	return g.Req
}

func (g GetGroupList) GetResp() interface{} {
	return g.Resp
}
