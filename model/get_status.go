package model

import "qq_bot/conf"

// NapCat：获取运行状态
//   POST {server}/get_status
//   doc:  napcat.apifox.cn -> 系统接口 -> 获取运行状态
//
// 项目启动时用它做连通性检查，data.online + data.good 同时为 true 才算健康。

type GetStatusReq struct {
	BaseReq
}

type GetStatusData struct {
	Online bool                   `json:"online"`
	Good   bool                   `json:"good"`
	Stat   map[string]interface{} `json:"stat,omitempty"`
}

type GetStatusResp struct {
	BaseResp
	Data GetStatusData `json:"data"`
}

func (r *GetStatusResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type GetStatus struct {
	Req  *GetStatusReq
	Resp *GetStatusResp
}

func (g GetStatus) Name() string {
	return conf.Cfg.Server.Address + "get_status"
}

func (g GetStatus) GetReq() interface{}  { return g.Req }
func (g GetStatus) GetResp() interface{} { return g.Resp }
