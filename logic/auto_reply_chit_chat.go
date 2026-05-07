// auto_reply_chit_chat.go：单条闲聊 / 非业务消息「立刻丢弃、不调 Kimi」，避免无意义等满 silence 窗口。
//
// 求助 / 可能带图的单条消息不参与本路径（有图或偏长一律走原 60s flush + LLM）。
//
// 判定原则纯关键词级（成本廉价、不做语义分析）：含价格 / 业务词的就保留进窗口，等 silence 触发
// 时由 Kimi 做真正的语义识别。**故意不再用正则尝试解析意图**——正则做语义不可靠，会让落库
// 数据被低置信度兜底污染。
package logic

import (
	"regexp"
	"strings"
	"unicode/utf8"

	zaplog "qq_bot/utils/zap"
)

var (
	reChitChatBizHint = regexp.MustCompile(
		`面议|二手|求助|有偿|已出|已找到|找到了|求到了|已求到|已求得|已买到|买到了|下架旧的|关闭提问|关闭求解答|求解答|求物品|收购|求购|拼车|代取|代课|转让|闲置|收「|收[^到]|^求\s*\S|^不要了|^不卖了|^不出了|^不需要了`)
	reChitChatPrice = regexp.MustCompile(
		`\d+(?:\.\d+)?\s*[元块￥¥]|\d+(?:\.\d+)?[rR]`)
)

func autoReplySnapshotHasImage(snap []autoReplyMsg) bool {
	for _, m := range snap {
		for _, seg := range m.Segments {
			if seg.Type == "image" {
				return true
			}
		}
	}
	return false
}

func autoReplyFlatHasImagePlaceholder(flat string) bool {
	return strings.Contains(flat, "[图片]")
}

// tryInstantSilentChitChat 若当前桶**仅一条**纯文字、且不含任何业务关键词，则清空桶并返回 true——
// 调用方应立即 return，不再排队等 scanOnce。
//
// 关键词命中即保留进窗口；真正的语义识别留给 silence 触发后的 Kimi。
// 有消歧 / 去重 follow-up 待命时**不**丢弃，避免卡住后续「1」或「下架旧的」。
func tryInstantSilentChitChat(key autoReplyBucketKey, b *autoReplyBucket) bool {
	if len(b.Msgs) != 1 {
		return false
	}
	if autoReplySnapshotHasImage(b.Msgs) {
		return false
	}
	if disambigMgr.Get(key) != nil || dupOffShelfMgr.Peek(key) != nil {
		return false
	}
	m := b.Msgs[0]
	flat := strings.TrimSpace(m.FlatText)
	if autoReplyFlatHasImagePlaceholder(flat) {
		return false
	}
	if utf8.RuneCountInString(flat) > 80 {
		return false
	}
	if reChitChatBizHint.MatchString(flat) || reChitChatPrice.MatchString(flat) {
		return false
	}

	b.Msgs = nil
	zaplog.Logger.Debugf("autoReply instant silent chit-chat drop group=%d user=%d text=%q",
		key.GroupID, key.UserID, truncateForLog(flat, 80))
	return true
}
