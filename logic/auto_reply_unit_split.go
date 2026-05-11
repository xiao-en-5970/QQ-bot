// auto_reply_unit_split.go —— 把 (group, user) 桶里累积的消息按"上架习惯"切分成多个 unit。
//
// 背景：以前整个桶（含多个不同商品、多次发图发文）一股脑丢给 Kimi 一次识别，模型在
// 单条 ambiguous 消息（"收 X 0r" / "出 X"）上下文不足时容易判断错（求购 vs 出售 反向）。
//
// 新切分规则（来自用户实际使用习惯）：
//
//   - **模式 A 先图后文**：`图 图 图 文`——text 是 unit 闭合信号，**立即 flush**
//   - **模式 B 先文后图**：`文 图 图 图`——text 起头，后面的图不断累积；**等 silence**（或下一个
//     text 出现）才 flush
//   - **纯图序列**`图 图 图`：不形成任何 unit；silence 触发时静默清空，**不调 Kimi**
//
// 每个 unit 至多含**一个** text（业务消息），加上它周围的图片。多个 text 等于多个 unit。
package logic

import (
	"strings"

	"qq_bot/model"
)

// hasMeaningfulText 判断这条群消息是否带"有意义的业务文字"——用作 unit 切分点。
//
// 业务文字判定：segments 里存在 type=="text" 且 trim 后非空。@xxx / [图片] / [表情] /
// [引用回复] 等占位符都不算业务文字（用户单纯 @ 别人或者只发表情不视为上架意图）。
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

// hasInlineImage 判断这条消息内部是否含 image segment。
//
// QQ 输入框可以在一条消息里同时塞 text 和 image（"图文混发"），这种消息本身就是完整
// 上架意图——无需等 silence。详见 splitBucketIntoUnits 的"图文同条快速路径"。
func hasInlineImage(msg autoReplyMsg) bool {
	for _, seg := range msg.Segments {
		if seg.Type == "image" {
			return true
		}
	}
	return false
}

// splitBucketIntoUnits 把桶里消息序列按用户上架习惯切分成多个 unit。
//
// 切分目标：每个 unit = `[图]* + 1 个 text + [图]*`，至多 1 个 text。
//
// 算法（线性扫描）维护两个状态：
//
//   - pendingImages：还没匹配到 text 的累积图（即将走"模式 A 闭合"）
//   - activeUnit  ：已经见到 text 起头、正在等图或下一个 text 决定边界的 unit（模式 B）
//
// 状态转移：
//
//   遇到 text：
//     - pendingImages 非空 → 模式 A 闭合：pendingImages + text → completed unit；清空 pendingImages
//     - 否则若 activeUnit 已有 → 上一个模式 B 闭合：activeUnit → completed；新 text 启动新 activeUnit
//     - 否则（桶空白）→ 新 activeUnit = [text]
//   遇到 image：
//     - activeUnit 已有 → 加入 activeUnit（模式 B 补图）
//     - 否则 → 加入 pendingImages（模式 A 等闭合 text）
//
// 返回 (completedUnits, tail)：
//
//   - completedUnits：边界已闭合的 unit，调用方应当立即异步 flush 识别
//   - tail：留在桶里的最后一段；可能是 activeUnit（含 text 的模式 B 半成品，等 silence）
//     或 pendingImages（纯图，调用方按 silence 后无 text 静默清空处理）
func splitBucketIntoUnits(msgs []autoReplyMsg) ([][]autoReplyMsg, []autoReplyMsg) {
	var completed [][]autoReplyMsg
	var pendingImages []autoReplyMsg
	var activeUnit []autoReplyMsg

	for _, m := range msgs {
		if hasMeaningfulText(m) {
			// 图文同条快速路径：消息内部已经同时含 text+image，本身就是完整上架意图，
			// **自包含**——不吸收周围任何图（前置孤图很可能是无关闲聊，不能误归属）。
			//
			//   - 前置 pendingImages（孤图）→ 直接丢弃
			//   - 之前等图的 activeUnit → 单独闭合（不吸收这条图文同条）
			//   - 图文同条本身 → 单独成一个 completed unit
			if hasInlineImage(m) {
				pendingImages = nil
				if len(activeUnit) > 0 {
					completed = append(completed, activeUnit)
					activeUnit = nil
				}
				completed = append(completed, []autoReplyMsg{m})
				continue
			}
			switch {
			case len(pendingImages) > 0:
				// 模式 A 闭合：累积图 + 当前 text → 完成的 unit。text 本身是右边界，
				// 不启动新 unit；下一条消息才可能开启新累积。
				unit := make([]autoReplyMsg, 0, len(pendingImages)+1)
				unit = append(unit, pendingImages...)
				unit = append(unit, m)
				completed = append(completed, unit)
				pendingImages = nil
			case len(activeUnit) > 0:
				// 上一个 text 在等图（模式 B 半成品），又来一个 text → 上一个 unit 立即闭合，
				// 新 text 启动新模式 B unit
				completed = append(completed, activeUnit)
				activeUnit = []autoReplyMsg{m}
			default:
				// 桶空白 / 上一 unit 刚被闭合 → text 起头，进入模式 B 等图
				activeUnit = []autoReplyMsg{m}
			}
			continue
		}
		// 非业务文字：当作 image 累积
		if len(activeUnit) > 0 {
			activeUnit = append(activeUnit, m)
		} else {
			pendingImages = append(pendingImages, m)
		}
	}

	// 扫描完后留在桶里的 tail：
	//   - activeUnit 非空（模式 B 半成品）→ 等 silence，到时整段识别
	//   - pendingImages 非空（纯图无 text）→ 等 silence，到时静默清空（不调 Kimi）
	//   - 两者都空 → 空 tail
	var tail []autoReplyMsg
	switch {
	case len(activeUnit) > 0:
		tail = activeUnit
	case len(pendingImages) > 0:
		tail = pendingImages
	}
	return completed, tail
}

// bucketHasMeaningfulText 桶里是否存在任何业务文字消息——给 scanOnce 用：silence 触发时
// 如果 tail 里全是图（无 text），就静默清空不识别。
func bucketHasMeaningfulText(msgs []autoReplyMsg) bool {
	for _, m := range msgs {
		if hasMeaningfulText(m) {
			return true
		}
	}
	return false
}
