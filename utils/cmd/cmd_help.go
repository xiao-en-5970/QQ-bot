package cmd

import (
	"net/http"
	"qq_bot/global"
	"qq_bot/logic"
)

func CmdHelp(client *http.Client, argv []string, group_id int64, user_id int64) (err error) {
	if len(argv) < 2 {
		return logic.SendGroupText(client, group_id, global.ErrCmdMenu)
	}
	switch argv[1] {
	case "help":
		return logic.SendGroupText(client, group_id, global.ErrCmdHelpHelp)
	case "jm":
		return logic.SendGroupText(client, group_id, global.ErrCmdJmHelp)
	case "pix":
		return logic.SendGroupText(client, group_id, global.ErrCmdPixHelp)
	}
	return nil
}
