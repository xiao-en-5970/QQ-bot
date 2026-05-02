package logic

import (
	"errors"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/model"
	ser "qq_bot/service"
	zaplog "qq_bot/utils/zap"
	"strconv"
)

// GetNewAtMessage 拉一次 NapCat /get_group_msg_history，识别 @bot 指令并放入解析队列。
//
// 主要规则：
//   - segment[0].Type 必须是 "at"
//   - at.qq 必须解析成当前 bot 的 user_id
//   - segment[1] 必须是 "text"，作为指令字符串
//   - 单独 @bot（无后续 segment）视为求帮助
//
// LatestSeq 用于游标：第一次调用时初始化为「上一条 - 1」防止处理历史消息。
func GetNewAtMessage(client *http.Client, groupID int64, latestSeq *int64) error {
	err, resp := ser.GetGroupMsgHistory(client, &model.GetGroupMsgHistoryReq{
		GroupID: groupID,
	})
	if err != nil {
		zaplog.Logger.Errorf("get_group_msg_history failed group=%d: %v", groupID, err)
		return err
	}

	messages := resp.Data.Messages
	length := len(messages)
	if length == 0 {
		zaplog.Logger.Debugf("群历史消息为空, group=%d", groupID)
		*latestSeq = 0
		return errors.New("slice is empty")
	}

	tailSeq := messages[length-1].MessageSeq

	if *latestSeq == 0 {
		*latestSeq = tailSeq - 1
		zaplog.Logger.Debugf("LatestSeq init group=%d seq=%d", groupID, *latestSeq)
	}
	if *latestSeq == tailSeq {
		return nil
	}

	for _, msg := range messages {
		if msg.MessageSeq <= *latestSeq {
			continue
		}
		if len(msg.Message) == 0 {
			continue
		}

		first := msg.Message[0]
		if first.Type != "at" {
			continue
		}
		atData, _ := model.AsAtData(first.Data)
		if atData.QQ == "all" {
			continue
		}

		// 单独 @bot 视为求帮助
		if len(msg.Message) == 1 {
			zaplog.Logger.Debugf("group=%d 单独 @bot, return menu", groupID)
			_ = SendGroupAtText(client, groupID, msg.UserID, global.ErrCmdMenu)
			continue
		}

		atID, parseErr := strconv.ParseInt(atData.QQ, 10, 64)
		if parseErr != nil {
			zaplog.Logger.Errorf("解析 at.qq 失败: %v, raw=%s", parseErr, atData.QQ)
			continue
		}
		if atID != *conf.Cfg.User.UserID {
			continue
		}

		second := msg.Message[1]
		if second.Type != "text" {
			zaplog.Logger.Debugf("group=%d @bot 后非文本 (type=%s)，return menu", groupID, second.Type)
			_ = SendGroupAtText(client, groupID, msg.UserID, global.ErrCmdArgFault)
			continue
		}
		td, decErr := model.AsTextData(second.Data)
		if decErr != nil {
			zaplog.Logger.Errorf("解析 text segment 失败: %v", decErr)
			continue
		}
		if td.Text == "" || td.Text == " " {
			continue
		}
		zaplog.Logger.Debugf("group=%d 收到指令: %s", groupID, td.Text)

		global.ChanToParseCmd <- model.ChanToParseCmd{
			GroupID: groupID,
			UserID:  msg.UserID,
			Data:    td,
		}
	}

	*latestSeq = tailSeq
	return nil
}
