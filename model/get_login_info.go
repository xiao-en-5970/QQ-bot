package model

import "qq_bot/conf"

// NapCat：获取登录号信息
//   POST {server}/get_login_info
//   doc:  napcat.apifox.cn -> 系统接口 -> 获取登录号信息
//
// 请求体为空，data 字段对齐 OB11User 子集。

type GetLoginInfoReq struct {
	BaseReq
}

type GetLoginInfoData struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
}

type GetLoginInfoResp struct {
	BaseResp
	Data GetLoginInfoData `json:"data"`
}

func (r *GetLoginInfoResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type GetLoginInfo struct {
	Req  *GetLoginInfoReq
	Resp *GetLoginInfoResp
}

func (g GetLoginInfo) Name() string {
	return conf.Cfg.Server.Address + "get_login_info"
}

func (g GetLoginInfo) GetReq() interface{}  { return g.Req }
func (g GetLoginInfo) GetResp() interface{} { return g.Resp }
