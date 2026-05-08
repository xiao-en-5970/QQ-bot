// auto_reply_chit_chat.go：辅助工具——只判断"快照里是否含图片 / 是否含图片占位符"，
// 给 dup_off_shelf_state 等路径调用。
//
// 之前这里有一套基于关键词正则的"闲聊预过滤"，会在窗口聚合阶段把短文本立刻丢掉以避免触发 Kimi。
// 实际跑下来发现：
//
//   - 关键词白名单永远不全（"上架一个 X"、"出闲置"、"低价转 Y" 等口语化句式频频被误删）；
//   - 用户对"我说了为什么 bot 没识别"非常敏感，体验比省 Kimi quota 更重要；
//   - quota 冷却已经有静默兜底（skill/bot/recognition.md "LLM 配额熔断 + 静默丢弃"），
//     不依赖正则填补。
//
// 因此整个预过滤已经移除：所有消息一律进窗口，等 silence 触发后由 Kimi 统一判定。
package logic

// autoReplySnapshotHasImage 快照里是否含图片段。
//
// 仅供 dup_off_shelf_state 路径用——"下架旧的"必须是"短纯文本"，
// 一旦带图就当作普通业务消息，不能匹配 follow-up 短指令。
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
