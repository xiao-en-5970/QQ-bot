package model

import "qq_bot/conf"

type CanSendImageReq struct {
	BaseReq
}
type CanSendImageData struct {
	Yes bool `json:"yes"`
}

type CanSendImageResp struct {
	BaseResp
	CanSendImageData CanSendImageData `json:"data"`
}

type CanSendImage struct {
	Req  *CanSendImageReq
	Resp *CanSendImageResp
}

func (g CanSendImage) Name() string {
	return conf.Cfg.Server.Address + "can_send_image"
}

func (g CanSendImage) GetReq() interface{} {
	return g.Req
}

func (g CanSendImage) GetResp() interface{} {
	return g.Resp
}
