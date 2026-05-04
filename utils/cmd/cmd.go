package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/model"
	"qq_bot/utils/client_pool"
	zaplog "qq_bot/utils/zap"
	"strings"
)

// ParseCmd 必须由 main 在 `go ParseCmd(...)` 之前调用 global.Wg.Add(1)，
// 协程内部只负责 Done（避免 main 抢跑 Wg.Wait race，详见 main.go 注释）。
func ParseCmd(ctx context.Context) {
	defer global.Wg.Done()
	zaplog.Logger.Debugf("协程ParseCmd启动")
	defer zaplog.Logger.Debugf("协程ParseCmd退出")
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

// knownCmds 列出 ExecCmd switch 里"显式 case"的命令名。
// dataSlice[0] 命中这里 = 已识别命令；不命中 = 走 commands.default 兜底或显示菜单。
var knownCmds = map[string]bool{
	"jm":     true,
	"github": true,
	"help":   true,
	"pix":    true,
	"chat":   true,
}

func ExecCmd(chanParseCmd model.ChanToParseCmd, client *http.Client) {
	global.Wg.Add(1)
	zaplog.Logger.Debugf("协程ExecCmd启动(GroupID:%d):%s", chanParseCmd.GroupID, chanParseCmd.Data.Text)
	defer zaplog.Logger.Debugf("协程ExecCmd(GroupID:%d)退出%s", chanParseCmd.GroupID, chanParseCmd.Data.Text)
	defer global.Wg.Done()
	zaplog.Logger.Debugf("<- global.ChanToJm，%#v", len(global.ChanToParseCmd))
	chanParseCmd.Data.Text = strings.TrimSpace(chanParseCmd.Data.Text)
	dataSlice := strings.Split(chanParseCmd.Data.Text, " ")

	// 第 1 步：把"未匹配关键字"归一化成 commands.default 指定的命令，方便后面统一走 switch。
	// 例如用户 `@bot 你好`、配置 default=chat，则 dataSlice 被 prepend 成 ["chat","你好"]，
	// 后续完全按显式 `@bot chat 你好` 的链路处理，子命令逻辑不需要任何特殊分支。
	cmdName := dataSlice[0]
	if !knownCmds[cmdName] {
		def := strings.TrimSpace(conf.Cfg.Commands.Default)
		if def == "" {
			// 没配 default → 用菜单兜底（菜单本身已经按 IsEnabled 过滤了禁用项）。
			// 全禁用时菜单返回 ""，此时彻底闭嘴，连菜单都不发，跟"严格 opt-in"语义一致。
			if menu := global.Menu(); menu != "" {
				_ = logic.SendGroupAtText(client, chanParseCmd.GroupID, chanParseCmd.UserID, menu)
			}
			return
		}
		dataSlice = append([]string{def}, dataSlice...)
		cmdName = def
	}

	// 第 2 步：白名单检查。被禁用的命令直接静默 return（debug 日志留痕方便排查）。
	if !conf.Cfg.Commands.IsEnabled(cmdName) {
		zaplog.Logger.Debugf("group=%d user=%d 子命令 %q 不在 commands.enabled 白名单中，静默忽略",
			chanParseCmd.GroupID, chanParseCmd.UserID, cmdName)
		return
	}

	var err error
	switch cmdName {
	case "jm":
		err = CmdJm(client, dataSlice, chanParseCmd.GroupID, chanParseCmd.UserID)
	case "github":
		err = CmdGithub(client, chanParseCmd.GroupID, chanParseCmd.UserID)
	case "help":
		err = CmdHelp(client, dataSlice, chanParseCmd.GroupID, chanParseCmd.UserID)
	case "pix":
		err = CmdPix(client, dataSlice, chanParseCmd.GroupID)
	case "chat":
		// chatText 是去掉关键字 "chat" 后的纯用户文本：
		//   - `@bot chat 你好` → dataSlice=["chat","你好"] → chatText="你好"
		//   - default=chat 时 `@bot 你好` → 上面 prepend 后 dataSlice=["chat","你好"] → chatText="你好"
		chatText := strings.Join(dataSlice[1:], " ")
		err = CmdChat(client, chanParseCmd.GroupID, chanParseCmd.UserID, chatText)
	default:
		// 走到这里说明 knownCmds 加了新关键字但 switch 忘了对应 case，是开发期 bug。
		zaplog.Logger.Errorf("ExecCmd: knownCmds 包含 %q 但 switch 没有 case，这是 bug", cmdName)
		return
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
