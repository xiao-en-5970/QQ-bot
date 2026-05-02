package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/model"
	"qq_bot/utils/client_pool"
	zaplog "qq_bot/utils/zap"
	"strings"
)

// 解析指令
func ParseCmd(ctx context.Context) {
	global.Wg.Add(1)
	zaplog.Logger.Debugf("协程ParseCmd启动")
	defer zaplog.Logger.Debugf("协程ParseCmd退出")
	defer global.Wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case chanParseCmd, ok := <-global.ChanToParseCmd:
			if ok {
				global.ThreadPool.Submit(func() {
					ExecCmd(chanParseCmd, client_pool.NewClientPool())
				})

			}
		}
	}

}

func ExecCmd(chanParseCmd model.ChanToParseCmd, client *http.Client) {
	global.Wg.Add(1)
	zaplog.Logger.Debugf("协程ExecCmd启动(GroupID:%d):%s", chanParseCmd.GroupID, chanParseCmd.Data.Text)
	defer zaplog.Logger.Debugf("协程ExecCmd(GroupID:%d)退出%s", chanParseCmd.GroupID, chanParseCmd.Data.Text)
	defer global.Wg.Done()
	zaplog.Logger.Debugf("<- global.ChanToJm，%#v", len(global.ChanToParseCmd))
	chanParseCmd.Data.Text = strings.TrimSpace(chanParseCmd.Data.Text)
	dataSlice := strings.Split(chanParseCmd.Data.Text, " ")
	var err error
	switch dataSlice[0] {
	case "jm":
		err = CmdJm(client, dataSlice, chanParseCmd.GroupID, chanParseCmd.UserID)
	case "github":
		err = CmdGithub(client, chanParseCmd.GroupID, chanParseCmd.UserID)
	case "help":
		err = CmdHelp(client, dataSlice, chanParseCmd.GroupID, chanParseCmd.UserID)
	case "pix":
		err = CmdPix(client, dataSlice, chanParseCmd.GroupID)
	default:
		// 默认走 Kimi 聊天（如果未启用则在 CmdDefault 内回退到菜单）
		err = CmdDefault(client, chanParseCmd.GroupID, chanParseCmd.UserID, chanParseCmd.Data.Text)
	}
	if err != nil {
		zaplog.Logger.Error(err)
		// 子命令已经自己 SendGroupAtText/SendGroupText 解释过的错误，包了 ErrUserNotified。
		// 这里识别一下就不再追加回执，避免群里看到两条提示。
		if errors.Is(err, global.ErrUserNotified) {
			return
		}
		// 其余的「子命令没向用户解释」的错误，统一在这里兜底回执，避免静默失败。
		brief := briefError(err)
		feedback := fmt.Sprintf("指令 %q 执行失败: %s", firstWord(chanParseCmd.Data.Text), brief)
		_ = logic.SendGroupAtText(client, chanParseCmd.GroupID, chanParseCmd.UserID, feedback)
	}
}

// briefError 把一个可能很长 / 多行的 err 压成单行短文本，方便丢回群里。
//
// 规则：
//   - 取首行（过滤掉 stack-like 续行 / 多重 wrap 的换行）
//   - 按 rune 截断到 100 字符，超过加 "..."（避免中文截半个字）
func briefError(err error) string {
	s := err.Error()
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	runes := []rune(s)
	const max = 100
	if len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return s
}

// firstWord 取出指令文本的第一个 token（指令名），用来回执时让用户知道是哪条指令挂了。
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}
