package model

import "qq_bot/conf"

// NapCat：是否可以发送图片
//   POST {server}/can_send_image
//   doc:  napcat.apifox.cn -> 系统接口 -> 是否可以发送图片
//
// 请求体为空，响应 data 只有一个 yes 字段。

type CanSendImageReq struct {
	BaseReq
}

type CanSendImageData struct {
	Yes bool `json:"yes"`
}

type CanSendImageResp struct {
	BaseResp
	Data CanSendImageData `json:"data"`
}

func (r *CanSendImageResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type CanSendImage struct {
	Req  *CanSendImageReq
	Resp *CanSendImageResp
}

func (g CanSendImage) Name() string {
	return conf.Cfg.Server.Address + "can_send_image"
}

func (g CanSendImage) GetReq() interface{}  { return g.Req }
func (g CanSendImage) GetResp() interface{} { return g.Resp }
