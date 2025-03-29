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
	chapter := int64(1)
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

		if len(dataSlice) > 2 {
			chapter, err = strconv.ParseInt(dataSlice[2], 10, 64)
			if err != nil {
				zaplog.Logger.Warnf("Arg parse error: %v", err)
				return err
			}
		}

		_ = SendGroupMsg(client, group_id, userID, fmt.Sprintf("%s %d 第 %d 章", global.InfoCmdJmFindingBook, num, chapter))
		global.ChanToJm <- model.ChanToJM{GroupID: group_id, Number: num, Chapter: chapter, UserID: userID}
		zaplog.Logger.Debugf("global.ChanToJm <- ，%#v", len(global.ChanToJm))
	case "github":
		_ = SendGroupMsg(client, group_id, userID, "项目已开源：https://github.com/xiao-en-5970/QQ-bot")
	case "help":
		_ = SendGroupMsg(client, group_id, userID, global.ErrCmdMenu)
	default:
		if err = SendGroupMsg(client, group_id, userID, fmt.Sprintf("%s\n%s", global.ErrCmdNotFound, global.ErrCmdMenu)); err != nil {
			return err
		}
	}
	return nil
}
