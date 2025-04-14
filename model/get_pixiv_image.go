package model

import "qq_bot/conf"

type GetPixivImageReq struct {
	BaseReq
}
type GetPixivImageData struct {
	Pid    int64    `json:"pid"`
	Page   int      `json:"page"`
	Uid    int64    `json:"uid"`
	Title  string   `json:"title"`
	User   string   `json:"user"`
	R18    int      `json:"r18"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
	Tags   []string `json:"tags"`
	Url    string   `json:"url"`
}
type GetPixivImageResp []GetPixivImageData

type GetPixivImage struct {
	Req  *GetPixivImageReq
	Resp *GetPixivImageResp
}

func (g GetPixivImage) Name() string {
	return conf.Cfg.Pixiv.PixivAddress
}

func (g GetPixivImage) GetReq() interface{} {
	return g.Req
}

func (g GetPixivImage) GetResp() interface{} {
	return g.Resp
}
