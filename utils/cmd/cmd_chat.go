package cmd

import (
	"context"
	"net/http"
	"qq_bot/global"
	"qq_bot/logic"
	zaplog "qq_bot/utils/zap"
	"strings"
	"time"
)

// CmdChat 处理 `@bot chat <想说的话>`，走 Kimi（Moonshot）聊天。
//
// 调用语义：
//   - 由 ExecCmd 在两种情况下调用：
//     a) 用户明确写了 `@bot chat ...`（dataSlice[0] == "chat"）
//     b) 用户输入未匹配任何已知命令、且 `commands.default == "chat"`，被 ExecCmd
//        在 dataSlice 头部 prepend 了 "chat" 后转发过来
//
// chatText 入参语义：
//   - 已经剥掉了"chat"关键字本身的纯用户文本，即 strings.Join(dataSlice[1:], " ")
//   - 来自情况 (a) 时是关键字后面那段，来自情况 (b) 时就是用户输入全部
//
// 行为：
//   - global.Kimi == nil（GPT_API_KEY 未配置）-> 明确告知聊天功能未启用，不再静默回菜单
//     避免管理员忘配 key 时用户对着空菜单一头雾水
//   - chatText 为空（比如只发了 `@bot chat`）-> 提示需要带话
//   - 正常情况调 Kimi.Chat，60s 超时；Kimi 报错时回一句"我走神了"避免抛栈
func CmdChat(client *http.Client, groupID int64, userID int64, chatText string) error {
	if global.Kimi == nil {
		return logic.SendGroupAtText(client, groupID, userID, global.ErrCmdChatDisabled)
	}
	chatText = strings.TrimSpace(chatText)
	if chatText == "" {
		return logic.SendGroupAtText(client, groupID, userID, global.ErrCmdChatHelp)
	}

	// moonshot 单次响应有时较长，给 60s 超时
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	answer, err := global.Kimi.Chat(ctx, userID, chatText)
	if err != nil {
		zaplog.Logger.Errorf("kimi chat failed group=%d user=%d: %v", groupID, userID, err)
		return logic.SendGroupAtText(client, groupID, userID, global.ErrCmdChatKimiFault)
	}
	return logic.SendGroupAtText(client, groupID, userID, global.InfoCmdChatChatPrefix+answer)
}
