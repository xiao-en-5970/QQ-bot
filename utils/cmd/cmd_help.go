package cmd

import (
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
)

// CmdHelp 处理 `@bot help` / `@bot help <cmd>`。
//
// 当用户问的是被白名单禁用的子命令（例如 jm 被关掉时 `@bot help jm`），
// 静默 return —— 跟 ExecCmd 那边"禁用就闭嘴"的语义保持一致。
func CmdHelp(client *http.Client, argv []string, group_id int64, user_id int64) (err error) {
	if len(argv) < 2 {
		return logic.SendGroupText(client, group_id, global.Menu())
	}
	target := argv[1]
	// 询问被禁用的子命令时静默不回，避免菜单里看不到的功能反而能从 help 里看到帮助。
	if !conf.Cfg.Commands.IsEnabled(target) {
		return nil
	}
	switch target {
	case "help":
		return logic.SendGroupText(client, group_id, global.ErrCmdHelpHelp)
	case "jm":
		return logic.SendGroupText(client, group_id, global.ErrCmdJmHelp)
	case "pix":
		return logic.SendGroupText(client, group_id, global.ErrCmdPixHelp)
	case "chat":
		return logic.SendGroupText(client, group_id, global.ErrCmdChatHelp)
	}
	return nil
}
