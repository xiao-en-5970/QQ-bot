package model

import "qq_bot/conf"

// NapCat：获取好友列表
//
//	POST {server}/get_friend_list
//	doc:  napcat.apifox.cn -> 个人接口 -> 获取好友列表
//
// 用途：QQ 绑定流程要先校验"目标 QQ 是不是 bot 好友"——不是的话发不了私聊验证码。
// 列表数据量小（QQ 单号好友上限 5000），不分页一次拉回来；调用方按 user_id 线性查找。
//
// NapCat 缓存好友列表，no_cache=true 强制刷新；正常场景默认走缓存即可。

type GetFriendListReq struct {
	BaseReq
	NoCache bool `json:"no_cache,omitempty"`
}

// OB11Friend 字段参考 NapCat apifox -> 数据模型 -> OB11Friend。
//
// 字段比文档列出的更多，但绑定场景只关心 UserID；其它字段保留主流的方便排查。
type OB11Friend struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Remark   string `json:"remark,omitempty"`
	Sex      string `json:"sex,omitempty"`
	Level    int32  `json:"level,omitempty"`
}

type GetFriendListResp struct {
	BaseResp
	Data []OB11Friend `json:"data"`
}

func (r *GetFriendListResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type GetFriendList struct {
	Req  *GetFriendListReq
	Resp *GetFriendListResp
}

func (g GetFriendList) Name() string {
	return conf.Cfg.Server.Address + "get_friend_list"
}

func (g GetFriendList) GetReq() interface{}  { return g.Req }
func (g GetFriendList) GetResp() interface{} { return g.Resp }
