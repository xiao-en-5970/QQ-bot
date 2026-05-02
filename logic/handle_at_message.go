package logic

import (
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/model"
	zaplog "qq_bot/utils/zap"
	"strconv"
	"strings"
)

// HandleAtMessage 处理一条 OneBot11 群消息事件，识别 @bot 指令并转发到 ChanToParseCmd。
//
// 这个函数被 utils/wsclient 收到 NapCat 推送的 group message 事件时调用。
// 与原先轮询版（GetNewAtMessage）共用同一套规则，行为保持一致：
//
// 防重复 / 防回响：
//  1. bot 自己发的消息直接跳过（防 bot 回复自己）
//  2. message_id 走全局 LRU（global.ProcessedMsgIDs）去重
//     —— WS 单连接下 NapCat 不会重复推同一事件，但断线重连 / 跑两个 bot 实例时仍能兜底
//
// @bot 识别规则：
//   - segment[0].Type == "at" 且 at.qq == bot_id 才算 @bot
//   - at.qq == "all"（@全体）忽略，不算指令
//   - 单独 @bot（无后续 segment）-> 回菜单
//   - segment[1].Type == "text" 才算指令文本，TrimSpace 后非空才入队
//   - segment[1] 是图片/face/reply 等非文本，回参数错误提示
//
// 关于 channel send：
//
//	这里写 ChanToParseCmd 是阻塞的，依赖下游 ParseCmd + ants pool 的反压。
//	WS 模式下消息是 NapCat 实时单条推过来的，不会出现"批回放"挤爆队列的情况，
//	阻塞在这里属于自然削峰，不会卡住 WS 读循环（WS 协程比这里靠后一层在 wsclient 里）。
func HandleAtMessage(client *http.Client, msg *model.Message) {
	if msg == nil || msg.GroupID == 0 {
		return
	}

	if conf.Cfg.User.UserID == nil {
		zaplog.Logger.Errorf("HandleAtMessage 收到事件但 bot user_id 未初始化，丢弃 msgid=%d", msg.MessageID)
		return
	}
	botID := *conf.Cfg.User.UserID

	if msg.UserID == botID {
		return
	}

	if !global.ProcessedMsgIDs.Add(msg.MessageID) {
		zaplog.Logger.Debugf("group=%d msgid=%d 已处理过，跳过(防重复)", msg.GroupID, msg.MessageID)
		return
	}

	if len(msg.Message) == 0 {
		return
	}

	first := msg.Message[0]
	if first.Type != "at" {
		return
	}
	atData, _ := model.AsAtData(first.Data)
	if atData.QQ == "all" {
		return
	}
	atID, parseErr := strconv.ParseInt(atData.QQ, 10, 64)
	if parseErr != nil {
		zaplog.Logger.Errorf("解析 at.qq 失败: %v, raw=%s", parseErr, atData.QQ)
		return
	}
	if atID != botID {
		return
	}

	if len(msg.Message) == 1 {
		zaplog.Logger.Debugf("group=%d msgid=%d 单独 @bot, return menu", msg.GroupID, msg.MessageID)
		_ = SendGroupAtText(client, msg.GroupID, msg.UserID, global.ErrCmdMenu)
		return
	}

	second := msg.Message[1]
	if second.Type != "text" {
		zaplog.Logger.Debugf("group=%d msgid=%d @bot 后非文本 (type=%s)，return menu",
			msg.GroupID, msg.MessageID, second.Type)
		_ = SendGroupAtText(client, msg.GroupID, msg.UserID, global.ErrCmdArgFault)
		return
	}
	td, decErr := model.AsTextData(second.Data)
	if decErr != nil {
		zaplog.Logger.Errorf("解析 text segment 失败: %v", decErr)
		return
	}
	text := strings.TrimSpace(td.Text)
	if text == "" {
		return
	}

	zaplog.Logger.Infof("group=%d msgid=%d user=%d 收到指令: %s",
		msg.GroupID, msg.MessageID, msg.UserID, text)

	global.ChanToParseCmd <- model.ChanToParseCmd{
		GroupID: msg.GroupID,
		UserID:  msg.UserID,
		Data:    model.TextData{Text: text},
	}
}
