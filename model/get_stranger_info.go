package model

import "qq_bot/conf"

// NapCat：获取陌生人信息——按 user_id 拿 QQ 全局昵称 / 性别 / 年龄等。
//
//	POST {server}/get_stranger_info
//	doc:  napcat.apifox.cn -> 个人接口 -> 获取陌生人信息
//
// 用途：旗下号 nickname 定期同步——bot 周期性拉所有旗下号的最新 QQ 昵称写回 hfut。
// 跟 get_group_member_info 不同：
//   - get_group_member_info 拿的是"群名片"（特定群里的备注名），需要 group_id 且 bot 在群里
//   - get_stranger_info 拿的是"QQ 全局昵称"（不依赖任何群），更适合做全局同步任务
//
// no_cache=true 强制刷新；同步任务建议每次都 no_cache=true 拿最新值。

type GetStrangerInfoReq struct {
	BaseReq
	UserID  int64 `json:"user_id"`
	NoCache bool  `json:"no_cache,omitempty"`
}

// OB11Stranger 字段参考 NapCat apifox -> 数据模型 -> OB11Stranger。
//
// 同步场景只关心 Nickname；其它字段保留以备扩展。
type OB11Stranger struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Sex      string `json:"sex,omitempty"`
	Age      int32  `json:"age,omitempty"`
}

type GetStrangerInfoResp struct {
	BaseResp
	Data OB11Stranger `json:"data"`
}

func (r *GetStrangerInfoResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type GetStrangerInfo struct {
	Req  *GetStrangerInfoReq
	Resp *GetStrangerInfoResp
}

func (g GetStrangerInfo) Name() string {
	return conf.Cfg.Server.Address + "get_stranger_info"
}

func (g GetStrangerInfo) GetReq() interface{}  { return g.Req }
func (g GetStrangerInfo) GetResp() interface{} { return g.Resp }
