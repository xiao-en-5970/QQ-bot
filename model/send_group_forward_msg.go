package model

import "qq_bot/conf"

// NapCat：发送合并转发（群聊）
//   POST {server}/send_group_forward_msg
//   doc: napcat.apifox.cn -> 消息接口 -> 发送合并转发(群聊)（go-cqhttp 兼容 API）
//
// 用途：bot 在群内 seek_goods 命中本校在售商品时，把"商品文字 + 商品图 + 联系方式"
// 打包成"聊天记录"卡片一次性发到群里，避免直接发 QQ 临时图片（QQ 聊天记录会过期，
// 而 OSS URL 是 hfut mirror 过的永久地址）。
//
// 协议限制（已确认）：NapCat / OneBot11 在 send_group_forward_msg 里，节点 data
// 的 user_id / nickname 字段允许填，但客户端实际渲染时**节点的发送者头像仍是 bot
// 自己**——无法伪造成原卖家。即便如此，把不同信息拆成多个节点（文字 / 图 / 联系
// 方式）展示效果仍比一条很长的消息+图清晰得多。
//
// 不支持引用真实历史消息（用 data.id 引用现有 message_id）的形式——本设计也
// 不需要：直接构造自定义节点更稳定（避免历史消息被删 / 跨群引用失败）。

type SendGroupForwardMsgReq struct {
	BaseReq
	GroupID  int64           `json:"group_id"`
	Messages []ForwardNodeIn `json:"messages"`
}

// ForwardNodeIn 单个合并转发节点；只支持"自定义节点"形态（不传 data.id 引用历史）。
type ForwardNodeIn struct {
	Type string              `json:"type"` // 固定 "node"
	Data ForwardNodeInData   `json:"data"`
}

// ForwardNodeInData 节点的发送者展示信息 + 内容。
//
// UserID / Nickname：协议层填来作为"显示参考"，但客户端实际只渲染 bot 头像（见
// 文件头注释）。仍然填——某些 NapCat 版本会把 nickname 展示在节点标题栏。
type ForwardNodeInData struct {
	UserID   int64            `json:"user_id"`
	Nickname string           `json:"nickname"`
	Content  []MessageSegment `json:"content"`
}

// SendGroupForwardMsgResp 返回 message_id（合并转发卡片本身在群里的 ID）。
type SendGroupForwardMsgRespData struct {
	MessageID int64 `json:"message_id"`
}

type SendGroupForwardMsgResp struct {
	BaseResp
	Data SendGroupForwardMsgRespData `json:"data"`
}

func (r *SendGroupForwardMsgResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type SendGroupForwardMsg struct {
	Req  *SendGroupForwardMsgReq
	Resp *SendGroupForwardMsgResp
}

func (g SendGroupForwardMsg) Name() string {
	return conf.Cfg.Server.Address + "send_group_forward_msg"
}

func (g SendGroupForwardMsg) GetReq() interface{}  { return g.Req }
func (g SendGroupForwardMsg) GetResp() interface{} { return g.Resp }
