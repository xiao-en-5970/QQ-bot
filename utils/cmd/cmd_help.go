package cmd

import (
	"net/http"
	"qq_bot/global"
	"qq_bot/logic"
)

func CmdHelp(client *http.Client, group_id int64, user_id int64) (err error) {
	return logic.SendGroupMsg(client, group_id, user_id, global.ErrCmdMenu)
}
