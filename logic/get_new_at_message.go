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
// 三道防线杜绝重复回复：
//  1. **初始化跳历史**：latestSeq==0 时只记录当前最大 message_seq 后立即返回
//     —— 避免 bot 一启动就把 N 分钟前用户的 @bot 全部回复一遍
//  2. **seq 单调推进**：每 tick 处理 seq > latestSeq 的消息，处理完更新到本批最大 seq
//     —— 即便 NapCat 偶发返回空也保持 *latestSeq 不变，绝不让指针倒退
//  3. **message_id 全局 LRU 去重**（global.ProcessedMsgIDs，容量 4096）
//     —— message_seq 在 NapCat 偶发会抖动，但 message_id 是稳定的全局唯一 id；
//        甚至有人不小心同时跑了两个 bot 实例也不会复发（先到先得）
//
// 其他防御措施：
//   - msg.UserID == botID 直接跳过（防 bot 回复自己）
//   - 主动按 message_seq 升序排（不依赖 NapCat 返回顺序）
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
		// 防线 1：bot 自己发的消息直接跳过（防 bot 回复自己）
		if msg.UserID == botID {
			continue
		}
		// 防线 3（前置）：message_id 已处理过则跳过 —— 这是兜底的最关键一道
		// Add 是原子的"check + mark"：返回 false = 之前已加过，true = 首次加入
		if !global.ProcessedMsgIDs.Add(msg.MessageID) {
			zaplog.Logger.Debugf("group=%d msgid=%d 已处理过，跳过(防重复)", groupID, msg.MessageID)
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
		// 防线 2：必须 @ 的就是 bot 自己（atID == botID）
		if atID != botID {
			continue
		}

		// 单独 @bot（无后续 segment）-> 求帮助
		if len(msg.Message) == 1 {
			zaplog.Logger.Debugf("group=%d msgid=%d 单独 @bot, return menu", groupID, msg.MessageID)
			_ = SendGroupAtText(client, groupID, msg.UserID, global.ErrCmdMenu)
			continue
		}

		second := msg.Message[1]
		if second.Type != "text" {
			zaplog.Logger.Debugf("group=%d msgid=%d @bot 后非文本 (type=%s)，return menu",
				groupID, msg.MessageID, second.Type)
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
		zaplog.Logger.Infof("group=%d msgid=%d user=%d 收到指令: %s",
			groupID, msg.MessageID, msg.UserID, text)

		global.ChanToParseCmd <- model.ChanToParseCmd{
			GroupID: groupID,
			UserID:  msg.UserID,
			Data:    model.TextData{Text: text},
		}
	}

	*latestSeq = maxSeq
	return nil
}
