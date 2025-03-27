package logic

import (
	"fmt"
	"net/http"
	"qq_bot/global"
	"qq_bot/model"
	zaplog "qq_bot/utils/zap"
	"strconv"
	"strings"
)

// 解析指令
func ParseCmd(client *http.Client, group_id int64, userID int64, data model.TextData) (err error) {
	data.Text = strings.TrimSpace(data.Text)
	dataSlice := strings.Split(data.Text, " ")

	switch dataSlice[0] {
	case "jm":
		if len(dataSlice) < 2 {
			zaplog.Logger.Warnf("Arg error: %v", err)
			_ = SendGroupMsg(client, group_id, userID, fmt.Sprintf("jm%s\n%s", global.ErrCmdArgFault, global.ErrCmdJmHelp))
			return nil
		}
		num, err := strconv.ParseInt(dataSlice[1], 10, 64)
		if err != nil {
			zaplog.Logger.Warnf("Arg parse error: %v", err)
			_ = SendGroupMsg(client, group_id, userID, fmt.Sprintf("jm%s\n%s", global.ErrCmdArgFault, global.ErrCmdJmHelp))
			return nil
		}
		_ = SendGroupMsg(client, group_id, userID, global.InfoCmdJmFindingBook+strconv.FormatInt(num, 10))
		global.ChanToJm <- num

	default:
		if err = SendGroupMsg(client, group_id, userID, fmt.Sprintf("%s\n%s", global.ErrCmdNotFound, global.ErrCmdMenu)); err != nil {
			return err
		}
	}
	return nil
}
