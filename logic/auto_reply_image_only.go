// auto_reply_image_only.go —— "纯图 OCR 上架" 分发路径。
//
// 触发：scanOnce 检测到桶沉默 + tail 里没有业务文字（只有图片）→ 把整桶交给本函数。
//
// 与 processSnapshot 的差异：
//   - 不调 Kimi 文本识别（K2 看不到图）
//   - 对**每张图独立**调一次 Moonshot vision API（kimi.RecognizeFromImage）
//   - vision 返回的 action 当成 publish_good 单独走 dispatchActionToHfut
//   - 不需要 image_message_ids 跨图归属逻辑（每张图一个 action）
//
// 配额冷却命中时直接静默丢弃整桶——跟 processSnapshot 行为一致。

package logic

import (
	"context"
	"errors"
	"fmt"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/model"
	"qq_bot/utils/client_pool"
	"qq_bot/utils/kimi"
	"qq_bot/utils/metrics"
	zaplog "qq_bot/utils/zap"
	"time"
)

// processImageOnlySnapshot 把"纯图桶"按每张图单独跑 vision OCR + dispatch。
//
// 失败模式：
//   - global.Kimi == nil       静默丢弃（与 processSnapshot 同）
//   - vision quota cooling     静默丢弃整桶
//   - 单张图 OCR 失败           log 后跳过，继续下一张
//   - 单张图 OCR 返回 type==none log 后跳过（图里没识别到商品信息）
//   - dispatch 失败             ack 走 logic 既有路径（fail/dup ack 等）
func (m *autoReplyManager) processImageOnlySnapshot(key autoReplyBucketKey, snap []autoReplyMsg) {
	if len(snap) == 0 {
		return
	}
	first := snap[0]
	last := snap[len(snap)-1]
	imageCount := countImageSegments(snap)
	zaplog.Logger.Infof("autoReply image-OCR flush group=%d user=%d card=%q msgs=%d images=%d duration=%s",
		key.GroupID, key.UserID, first.UserCard, len(snap), imageCount, last.Time.Sub(first.Time))

	if global.Kimi == nil {
		zaplog.Logger.Debugf("autoReply image-OCR group=%d user=%d Kimi 未启用，丢弃整桶",
			key.GroupID, key.UserID)
		return
	}
	if imageCount == 0 {
		// 理论上 scanOnce 已经过滤过纯图桶才会进来；防御性 return
		return
	}

	httpClient := client_pool.NewClientPool()
	verbose := conf.Cfg.Group.IsAutoReplyVerbose()

	// 限流：整桶纯图共用 1 个令牌——这是"一次用户行为"，跟文本路径对齐。
	// 触发限流时静默丢弃整桶（image-OCR 路径连发图都罕见，触发限流约等于刷屏）。
	if ok, retry := dispatchLimiter.Allow(key); !ok {
		metrics.IncRateLimit()
		zaplog.Logger.Warnf("autoReply image-OCR 限流命中 group=%d user=%d images=%d retry=%s",
			key.GroupID, key.UserID, imageCount, retry)
		if verbose {
			_ = SendGroupAtText(httpClient, key.GroupID, key.UserID,
				fmt.Sprintf("太快，%d 秒后再发", int(retry.Seconds())))
		}
		return
	}

	// 给整体 vision OCR 留宽一点的超时——每张图最多 30s（visionHTTPTimeout）。
	// 50 张图就是 25 分钟极端情况，但实际窗口里一般 1-3 张图就够。
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer parentCancel()

	successCount := 0
	for _, msg := range snap {
		for _, seg := range msg.Segments {
			if seg.Type != "image" {
				continue
			}
			img, err := model.AsImageData(seg.Data)
			if err != nil || img.URL == "" {
				continue
			}
			ackText, ackKind := runVisionOnOneImage(parentCtx, key, first.UserCard, msg, img.URL)
			if ackKind == ackKindSuccess {
				successCount++
			}
			if ackText == "" {
				continue
			}
			// 单图上架成功 / 失败 / 重复，按 verbose 规则发回执
			res := ackResult{Text: ackText, Kind: ackKind}
			if res.shouldEmit(verbose) {
				_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, ackText)
			}
		}
	}

	if successCount == 0 {
		zaplog.Logger.Infof("autoReply image-OCR group=%d user=%d 未识别到任何商品（%d 张图全部 OCR 失败或 type=none）",
			key.GroupID, key.UserID, imageCount)
	}
}

// runVisionOnOneImage 对单张图调 vision OCR + dispatch 上架；返回 ack 文案与等级。
//
// quota cooling 时返回 ackKindIgnore（让上层不发任何回执，跟纯文本路径同语义）。
func runVisionOnOneImage(
	ctx context.Context,
	key autoReplyBucketKey,
	userCard string,
	msg autoReplyMsg,
	imageURL string,
) (string, ackKind) {
	visionCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	action, err := global.Kimi.RecognizeFromImage(visionCtx, imageURL, msg.MessageID)
	switch {
	case err == nil:
		metrics.IncRecognize("success")
	case errors.Is(err, kimi.ErrQuotaCooling):
		metrics.IncRecognize("quota_cooling")
		zaplog.Logger.Infof("autoReply image-OCR group=%d user=%d quota 冷却中，静默丢弃此图 msg=%d",
			key.GroupID, key.UserID, msg.MessageID)
		return "", ackKindIgnore
	case kimi.IsQuotaError(err):
		metrics.IncRecognize("quota_cooling")
		zaplog.Logger.Warnf("autoReply image-OCR group=%d user=%d 配额耗尽（单次），静默 msg=%d",
			key.GroupID, key.UserID, msg.MessageID)
		return "", ackKindIgnore
	default:
		metrics.IncRecognize("fail")
		zaplog.Logger.Warnf("autoReply image-OCR group=%d user=%d 单图识别失败 msg=%d: %v",
			key.GroupID, key.UserID, msg.MessageID, err)
		return "", ackKindIgnore
	}

	if action == nil || action.Type == "none" {
		reason := ""
		if action != nil {
			reason = action.Reason
		}
		zaplog.Logger.Infof("autoReply image-OCR group=%d user=%d msg=%d 图里无商品信息: %s",
			key.GroupID, key.UserID, msg.MessageID, truncateForLog(reason, 100))
		return "", ackKindIgnore
	}
	if action.Type != "publish_good" {
		// 视觉 OCR 通道只允许产出 publish_good——理论上 prompt 已限制，再防御一道
		zaplog.Logger.Warnf("autoReply image-OCR group=%d user=%d msg=%d 返回非 publish_good 类型: %s",
			key.GroupID, key.UserID, msg.MessageID, action.Type)
		return "", ackKindIgnore
	}

	// 复用 dispatchActionToHfut：UpsertQQChild + publish_good 落库 + dup 处理。
	// skipRateLimit=true：整桶已经在 processImageOnlySnapshot 入口统一取过限流令牌，
	// 这里不再 per-image 计数（否则一桶 5 张图就会触发原 max=8 阈值）。
	// snap 只放当前这一条 msg：让 imageURLsFromSnap 能用 image_message_ids 还原 URL，
	// 也让 collectBotMessageIDs 能把 msg.MessageID 写到 goods.bot_message_ids（reply 反查需要）。
	res := dispatchActionToHfut(ctx, key, userCard, []autoReplyMsg{msg}, *action, true)
	return res.Text, res.Kind
}

// countImageSegments 数桶里 image segment 总数。仅作日志，不影响逻辑。
func countImageSegments(snap []autoReplyMsg) int {
	n := 0
	for _, m := range snap {
		for _, seg := range m.Segments {
			if seg.Type == "image" {
				n++
			}
		}
	}
	return n
}
