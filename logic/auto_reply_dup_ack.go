package logic

import (
	"context"
	"fmt"
	"strings"
	"time"

	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/utils/client_pool"
	zaplog "qq_bot/utils/zap"
)

// processDupOffShelfAck 消费「去重提示 → 下架旧的」follow-up；已处理则返回 true（不再走 Kimi）。
func processDupOffShelfAck(key autoReplyBucketKey, snap []autoReplyMsg) bool {
	if len(snap) != 1 || autoReplySnapshotHasImage(snap) {
		return false
	}
	if !matchesDupOffShelfReply(snap[0].FlatText) {
		return false
	}
	if global.Hfut == nil {
		return false
	}
	p := dupOffShelfMgr.Take(key)
	if p == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpClient := client_pool.NewClientPool()

	title := strings.TrimSpace(p.Title)
	show := "这件"
	if title != "" {
		show = title
	}

	if err := global.Hfut.OffShelfGood(ctx, p.GoodID, p.HfutUserID); err != nil {
		zaplog.Logger.Errorf("autoReply dup-followup OffShelfGood 失败 good=%d user=%d: %v",
			p.GoodID, p.HfutUserID, err)
		res := ackResult{
			Text: fmt.Sprintf("「%s」下架没成功，过会儿再试一次", show),
			Kind: ackKindFail,
		}
		verbose := conf.Cfg.Group.IsAutoReplyVerbose()
		if res.shouldEmit(verbose) {
			_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
		}
		return true
	}

	res := ackResult{Text: fmt.Sprintf("已下架「%s」", show), Kind: ackKindSuccess}
	verbose := conf.Cfg.Group.IsAutoReplyVerbose()
	if res.shouldEmit(verbose) {
		zaplog.Logger.Infof("autoReply dup-followup ack → group=%d user=%d: %s",
			key.GroupID, key.UserID, truncateForLog(res.Text, 200))
		_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
	}
	return true
}
