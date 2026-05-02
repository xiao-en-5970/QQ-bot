package logic

import (
	"errors"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/model"
	ser "qq_bot/service"
	zaplog "qq_bot/utils/zap"
	"sort"
	"strconv"
	"strings"
)

// GetNewAtMessage 拉一次 NapCat /get_group_msg_history，识别 @bot 指令并放入解析队列。
//
// 关键不变量：
//  1. 初始化（latestSeq==0）时只记录当前最大 message_seq，不处理任何启动前的历史消息
//     —— 避免 bot 一启动就把 N 分钟前用户的 @bot 全部回复一遍
//  2. 后续如果 NapCat 偶发返回空 messages，保持 *latestSeq 不变（绝不重置回 0）
//     —— 否则下一轮又会触发 init 把"当前最新一条"当成新的处理 → 同一条 @bot 反复回复
//  3. 处理前先按 message_seq 升序排
//     —— NapCat 不同版本/不同群的返回顺序未必一致，自己排一遍最稳
//  4. 双重过滤 bot 自己发的消息：sender.user_id != bot_id && at.qq == bot_id
//     —— 避免 bot 回复自己
//
// 规则：
//   - segment[0].Type == "at" 且 at.qq == bot_id 才算 @bot
//   - 单独 @bot（无后续 segment）回菜单
//   - segment[1].Type == "text" 才算指令文本，TrimSpace 后非空才入队
func GetNewAtMessage(client *http.Client, groupID int64, latestSeq *int64) error {
	err, resp := ser.GetGroupMsgHistory(client, &model.GetGroupMsgHistoryReq{
		GroupID: groupID,
		Count:   20,
	})
	if err != nil {
		zaplog.Logger.Errorf("get_group_msg_history failed group=%d: %v", groupID, err)
		return err
	}

	messages := resp.Data.Messages
	if len(messages) == 0 {
		// 群从未活跃 / NapCat 短暂抖动：
		//   - 还没初始化过 -> 让 GroupTicker 走重试-停止逻辑
		//   - 已经初始化过 -> 当作"本轮没有新消息"，绝不能重置 *latestSeq
		if *latestSeq == 0 {
			zaplog.Logger.Debugf("群历史消息为空, group=%d (未初始化)", groupID)
			return errors.New("group has no messages yet")
		}
		return nil
	}

	// 主动按 message_seq 升序排，避免依赖 NapCat 返回顺序
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].MessageSeq < messages[j].MessageSeq
	})
	maxSeq := messages[len(messages)-1].MessageSeq

	if *latestSeq == 0 {
		// 第一次拉到这个群的消息，只记录基线 seq，不回复任何启动前的历史
		*latestSeq = maxSeq
		zaplog.Logger.Debugf("LatestSeq init group=%d seq=%d (跳过启动前 %d 条历史)",
			groupID, *latestSeq, len(messages))
		return nil
	}

	if maxSeq <= *latestSeq {
		return nil
	}

	botID := *conf.Cfg.User.UserID
	for _, msg := range messages {
		if msg.MessageSeq <= *latestSeq {
			continue
		}
		// 第一道防线：bot 自己发的消息直接跳过
		if msg.UserID == botID {
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
		atID, parseErr := strconv.ParseInt(atData.QQ, 10, 64)
		if parseErr != nil {
			zaplog.Logger.Errorf("解析 at.qq 失败: %v, raw=%s", parseErr, atData.QQ)
			continue
		}
		// 第二道防线：必须是 @bot 自己（atID == botID）
		if atID != botID {
			continue
		}

		// 单独 @bot（无后续 segment）-> 求帮助
		if len(msg.Message) == 1 {
			zaplog.Logger.Debugf("group=%d msgseq=%d 单独 @bot, return menu", groupID, msg.MessageSeq)
			_ = SendGroupAtText(client, groupID, msg.UserID, global.ErrCmdMenu)
			continue
		}

		second := msg.Message[1]
		if second.Type != "text" {
			zaplog.Logger.Debugf("group=%d msgseq=%d @bot 后非文本 (type=%s)，return menu",
				groupID, msg.MessageSeq, second.Type)
			_ = SendGroupAtText(client, groupID, msg.UserID, global.ErrCmdArgFault)
			continue
		}
		td, decErr := model.AsTextData(second.Data)
		if decErr != nil {
			zaplog.Logger.Errorf("解析 text segment 失败: %v", decErr)
			continue
		}
		text := strings.TrimSpace(td.Text)
		if text == "" {
			continue
		}
		zaplog.Logger.Debugf("group=%d msgseq=%d 收到指令: %s", groupID, msg.MessageSeq, text)

		global.ChanToParseCmd <- model.ChanToParseCmd{
			GroupID: groupID,
			UserID:  msg.UserID,
			Data:    model.TextData{Text: text},
		}
	}

	*latestSeq = maxSeq
	return nil
}
