package model

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
