package logic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/utils/client_pool"
	"qq_bot/utils/hfut"
	zaplog "qq_bot/utils/zap"
)

// processDupOffShelfAck 消费「重复商品反问 → 用户选择」follow-up；已处理则返回 true（不再走 Kimi）。
//
// 反问后用户的合法回复（详见 dup_off_shelf_state.go::matchesDupOffShelfReply）：
//
//   - 1 / "重复上架"   → 调 PublishGood with Force=true（旧的保留，新的创建）
//   - 2 / "下架旧的"   → OffShelfGood(旧) + PublishGood(新)（一次替换完成）
//   - 其它 / 含图     → 不消费上下文（返 false 让 Kimi 走正常识别）；TTL 内不回选择就自然过期
func processDupOffShelfAck(key autoReplyBucketKey, snap []autoReplyMsg) bool {
	if len(snap) != 1 || autoReplySnapshotHasImage(snap) {
		return false
	}
	choice := matchesDupOffShelfReply(snap[0].FlatText)
	if choice == dupChoiceNone {
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

	switch choice {
	case dupChoiceRepublish:
		handleDupRepublish(ctx, httpClient, key, p)
	case dupChoiceOffShelfOld:
		handleDupOffShelfThenRepublish(ctx, httpClient, key, p)
	}
	return true
}

// handleDupRepublish 用户选"1 重复上架"——调 PublishGood with Force=true 跳过查重。
//
// 不下架旧商品；DB 里会同时存在两条同名商品。这是用户显式确认的"我就是要再发一份"。
func handleDupRepublish(ctx context.Context, httpClient *http.Client, key autoReplyBucketKey, p *dupOffShelfPending) {
	verbose := conf.Cfg.Group.IsAutoReplyVerbose()

	if p.OriginalReq == nil {
		// 历史状态可能没存 OriginalReq（理论上不会发生，防御性兜底）
		zaplog.Logger.Errorf("dup-followup: choice=republish 但 OriginalReq 为空 group=%d user=%d", key.GroupID, key.UserID)
		res := ackResult{Text: "重复上架失败：缺少原请求上下文", Kind: ackKindFail}
		if res.shouldEmit(verbose) {
			_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
		}
		return
	}

	req := *p.OriginalReq
	req.Force = true
	resp, err := global.Hfut.PublishGood(ctx, req)
	if err != nil {
		// 不应该再撞去重（Force=true 已跳过），其它错误正常 fail
		zaplog.Logger.Errorf("dup-followup republish 失败 user=%d title=%q: %v",
			p.HfutUserID, p.Title, err)
		res := ackResult{
			Text: fmt.Sprintf("「%s」重复上架失败，稍后再试", showTitleOr(p.Title, "这条")),
			Kind: ackKindFail,
		}
		if res.shouldEmit(verbose) {
			_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
		}
		return
	}

	// 上架成功——记录到 recentGoodMgr（给"不要了/不卖了"上下文化处理用）
	if resp != nil {
		recentGoodMgr.Save(key, p.HfutUserID, resp.GoodID, p.Title, int(p.OriginalReq.Category))
	}
	res := ackResult{
		Text: fmt.Sprintf("已重复上架「%s」（旧的保留）", showTitleOr(p.Title, "")),
		Kind: ackKindSuccess,
	}
	if res.shouldEmit(verbose) {
		zaplog.Logger.Infof("autoReply dup-followup republish ack → group=%d user=%d: %s",
			key.GroupID, key.UserID, truncateForLog(res.Text, 200))
		_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
	}
}

// handleDupOffShelfThenRepublish 用户选"2 下架旧的并上架"——
// 先 OffShelfGood 旧的，再 PublishGood 新的（不带 Force，因为旧的已下架查重不会命中）。
//
// 若下架旧的失败：放弃后续上架，让用户感知到失败，避免"老的还在售 + 新的也创建"两件商品并存的语义混乱。
func handleDupOffShelfThenRepublish(ctx context.Context, httpClient *http.Client, key autoReplyBucketKey, p *dupOffShelfPending) {
	verbose := conf.Cfg.Group.IsAutoReplyVerbose()
	title := showTitleOr(p.Title, "这条")

	if err := global.Hfut.OffShelfGood(ctx, p.GoodID, p.HfutUserID); err != nil {
		zaplog.Logger.Errorf("dup-followup OffShelfGood 失败 good=%d user=%d: %v",
			p.GoodID, p.HfutUserID, err)
		res := ackResult{
			Text: fmt.Sprintf("「%s」下架失败，稍后再试", title),
			Kind: ackKindFail,
		}
		if res.shouldEmit(verbose) {
			_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
		}
		return
	}

	// 下架成功后重新上架
	if p.OriginalReq == nil {
		// 没有原请求——只能给用户报告"已下架"，无法上架新的（沿用老行为）
		res := ackResult{Text: fmt.Sprintf("已下架「%s」", title), Kind: ackKindSuccess}
		if res.shouldEmit(verbose) {
			_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
		}
		return
	}

	req := *p.OriginalReq
	req.Force = false // 旧的已下架，查重不会再撞
	resp, err := global.Hfut.PublishGood(ctx, req)
	if err != nil {
		// 极小概率：刚下架成功但创建新的失败（网络抖动 / DB 错）。给用户明确反馈。
		var dup *hfut.DuplicateGoodInfo
		if errors.As(err, &dup) {
			zaplog.Logger.Warnf("dup-followup: 下架旧的后创建新的仍撞去重？user=%d title=%q existing=%d",
				p.HfutUserID, p.Title, dup.ExistingID)
		}
		zaplog.Logger.Errorf("dup-followup republish-after-offshelf 失败 user=%d title=%q: %v",
			p.HfutUserID, p.Title, err)
		res := ackResult{
			Text: fmt.Sprintf("已下架「%s」，但重新上架失败，请稍后手动重发", title),
			Kind: ackKindFail,
		}
		if res.shouldEmit(verbose) {
			_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
		}
		return
	}
	if resp != nil {
		recentGoodMgr.Save(key, p.HfutUserID, resp.GoodID, p.Title, int(p.OriginalReq.Category))
	}
	res := ackResult{
		Text: fmt.Sprintf("已下架旧「%s」并上架新的", title),
		Kind: ackKindSuccess,
	}
	if res.shouldEmit(verbose) {
		zaplog.Logger.Infof("autoReply dup-followup offshelf+republish ack → group=%d user=%d: %s",
			key.GroupID, key.UserID, truncateForLog(res.Text, 200))
		_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
	}
}

func showTitleOr(title, fallback string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return fallback
	}
	return t
}
