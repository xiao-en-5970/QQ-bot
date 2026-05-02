package cmd

import (
	"context"
	"fmt"
	"net/http"
	"qq_bot/global"
	"qq_bot/logic"
	zaplog "qq_bot/utils/zap"
	"time"
)

// CmdDefault 处理所有未匹配 jm/pix/help/github 的指令。
//
// 行为：
//   - 配置了 GPT_API_KEY -> 走 Kimi 聊天，把 bot 当牧濑红莉栖与用户对话
//   - 没配 GPT_API_KEY    -> 回退到打印「指令不存在 + 菜单」（与原 LLOneBot 行为一致）
func CmdDefault(client *http.Client, groupID int64, userID int64, text string) error {
	if global.Kimi == nil {
		return logic.SendGroupAtText(client, groupID, userID,
			fmt.Sprintf("%s\n%s", global.ErrCmdNotFound, global.ErrCmdMenu))
	}
	// moonshot 单次响应有时较长，给 60s 超时
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	answer, err := global.Kimi.Chat(ctx, userID, text)
	if err != nil {
		zaplog.Logger.Errorf("kimi chat failed group=%d user=%d: %v", groupID, userID, err)
		return logic.SendGroupAtText(client, groupID, userID, "我刚才走神了... 你再说一遍 (；´д｀)")
	}
	return logic.SendGroupAtText(client, groupID, userID, "\n"+answer)
}
