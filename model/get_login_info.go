package model

import "qq_bot/conf"

type GetLoginInfoReq struct {
	BaseReq
}

type GetLoginInfoData struct {
	UserId   int64  `json:"user_id"`
	NickName string `json:"nickname"`
}

type GetLoginInfoResp struct {
	BaseResp
	GetLoginInfoData GetLoginInfoData `json:"data"`
}

type GetLoginInfo struct {
	Req  *GetLoginInfoReq
	Resp *GetLoginInfoResp
}

func (g GetLoginInfo) Name() string {
	return conf.Cfg.Server.Address + "get_login_info"
}

func (g GetLoginInfo) GetReq() interface{} {
	return g.Req
}

func (g GetLoginInfo) GetResp() interface{} {
	return g.Resp
}
