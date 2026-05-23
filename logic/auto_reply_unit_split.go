// auto_reply_unit_split.go —— 桶级 helper（业务文字检测）。
//
// 历史背景：早期这个文件包含 `splitBucketIntoUnits` 算法——把桶里序列按"图 图 图
// 文 / 文 图 图 图"模式切成多个 unit 提前 flush，让上架回执秒回。
//
// 实际场景里用户在一次发布中常常图文交杂（"图 图 图 视频 商品描述长文 图 价格"），
// 早期切分会在第一段文字处截止把后续价格切到下一个 unit，经常导致价格丢失。
//
// 现在的策略：所有消息攒在桶里，等 60s 沉默触发后 scanOnce 整桶一次性送 Kimi 识别。
// Kimi 自己处理"多商品 + 图文交杂"的归一（详见 recognizeSystemPrompt 里"图文交杂
// 多商品"那节）。文件只保留两个 helper：
//   - hasMeaningfulText：单条消息是否含业务文字
//   - bucketHasMeaningfulText：scanOnce 沉默触发时用来选走 Kimi 还是走 vision OCR
//     （整桶没文字 → 走 vision OCR 逐图识别；详见 auto_reply_image_only.go）
package logic

import (
	"strings"

	"qq_bot/model"
)

// hasMeaningfulText 判断这条群消息是否带"有意义的业务文字"——业务文字 = segments
// 里存在 type=="text" 且 trim 后非空。@xxx / [图片] / [表情] / [引用回复] 等占位
// 符都不算业务文字。
func hasMeaningfulText(msg autoReplyMsg) bool {
	for _, seg := range msg.Segments {
		if seg.Type != "text" {
			continue
		}
		td, err := model.AsTextData(seg.Data)
		if err != nil {
			continue
		}
		if strings.TrimSpace(td.Text) != "" {
			return true
		}
	}
	return false
}

// bucketHasMeaningfulText 桶里是否存在任何业务文字消息——给 scanOnce 用：silence
// 触发时如果桶里全是图（无 text），就走 vision OCR 逐图识别；否则走 Kimi 文本识别。
func bucketHasMeaningfulText(msgs []autoReplyMsg) bool {
	for _, m := range msgs {
		if hasMeaningfulText(m) {
			return true
		}
	}
	return false
}
