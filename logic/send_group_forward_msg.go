package logic

import (
	"net/http"
	"qq_bot/conf"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// SendGroupForwardMsg 把一个"聊天记录"合并转发包发到群里。
//
// 调用方负责构造 ForwardNodeIn 数组——典型用法见 SeekGoodForwardCard：
//   - 第 1 个 node：商品文字描述（标题 / 价格 / 描述）
//   - 第 2..N 个 node：每张商品图一个 node（image segment）
//   - 末尾 node：联系方式（QQ / 提示 app 内搜索）
//
// 灰度静默 (SilentMode) 开启 + 目标群非运维群时直接返回 nil，不发 NapCat——与
// SendGroupAtText / SendGroupText / UploadGroupFile 同一套门控逻辑。
func SendGroupForwardMsg(client *http.Client, groupID int64, nodes []model.ForwardNodeIn) error {
	if silentSuppressGroup(groupID, "[forward]") {
		return nil
	}
	if len(nodes) == 0 {
		return nil
	}
	err, _ := service.SendGroupForwardMsg(client, &model.SendGroupForwardMsgReq{
		GroupID:  groupID,
		Messages: nodes,
	})
	if err != nil {
		zaplog.Logger.Errorf("napcat send_group_forward_msg failed group=%d nodes=%d: %v",
			groupID, len(nodes), err)
		return err
	}
	return nil
}

// BuildForwardNodeText 工厂函数：一个纯文本节点。
//
// UserID 默认填 bot 自己（conf.Cfg.User.UserID），Nickname 默认 "校园 Bot"。
// 协议层这两个字段当展示参考；客户端实际渲染时还是 bot 头像（见 model 注释）。
func BuildForwardNodeText(text string) model.ForwardNodeIn {
	return model.ForwardNodeIn{
		Type: "node",
		Data: model.ForwardNodeInData{
			UserID:   forwardNodeBotUserID(),
			Nickname: forwardNodeBotNickname(),
			Content: []model.MessageSegment{
				{Type: "text", Data: model.TextData{Text: text}},
			},
		},
	}
}

// BuildForwardNodeImage 工厂函数：一个图片节点。url 是公网可达 URL（OSS / CDN 都行）。
func BuildForwardNodeImage(url string) model.ForwardNodeIn {
	return model.ForwardNodeIn{
		Type: "node",
		Data: model.ForwardNodeInData{
			UserID:   forwardNodeBotUserID(),
			Nickname: forwardNodeBotNickname(),
			Content: []model.MessageSegment{
				{Type: "image", Data: model.ImageData{File: url, URL: url}},
			},
		},
	}
}

func forwardNodeBotUserID() int64 {
	if conf.Cfg.User.UserID != nil {
		return *conf.Cfg.User.UserID
	}
	return 0
}

func forwardNodeBotNickname() string {
	// conf.User 当前只存 UserID；展示用固定 "校园 Bot" 字样足够
	// （客户端实际渲染节点头像仍是 bot 本身，nickname 只是协议层附带的展示参考）
	return "校园 Bot"
}
