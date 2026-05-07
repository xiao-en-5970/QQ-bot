// auto_reply_chit_chat.go：单条闲聊 / 非业务消息「立刻丢弃、不调 Kimi」，避免无意义等满 silence 窗口。
//
// 求助 / 可能带图的单条消息不参与本路径（有图或偏长一律走原 60s flush + LLM）。
package logic

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"qq_bot/utils/kimi"
	zaplog "qq_bot/utils/zap"
)

var (
	reChitChatBizHint = regexp.MustCompile(
		`面议|二手|求助|有偿|已出|已找到|下架旧的|关闭提问|收购|求购|拼车|代取|代课|转让|闲置`)
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

// tryInstantSilentChitChat 若当前桶**仅一条**纯文字、且不像任何业务信号、regex 兜底也认不出动作，
// 则清空桶并返回 true——调用方应立即 return，不再排队等 scanOnce。
//
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

	input := buildRecognizeInput(key, m.UserCard, b.Msgs)
	if reg := kimi.RecognizeViaRegex(input); len(reg.Actions) > 0 {
		return false
	}

	b.Msgs = nil
	zaplog.Logger.Debugf("autoReply instant silent chit-chat drop group=%d user=%d text=%q",
		key.GroupID, key.UserID, truncateForLog(flat, 80))
	return true
}
