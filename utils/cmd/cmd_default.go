package cmd

import (
	"fmt"
	"net/http"
	"qq_bot/global"
	"qq_bot/logic"
)

func CmdDefault(client *http.Client, group_id int64, user_id int64) (err error) {
	return logic.SendGroupAtText(client, group_id, user_id, fmt.Sprintf("%s\n%s", global.ErrCmdNotFound, global.ErrCmdMenu))
}
