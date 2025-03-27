package logic

import (
	"net/http"
	"qq_bot/cmd"
	"qq_bot/global"
	"qq_bot/model"
	zaplog "qq_bot/utils/zap"
	"strconv"
	"strings"
)

func ParseCmd(client *http.Client, data model.TextData) (err error) {
	data.Text = strings.TrimSpace(data.Text)
	dataSlice := strings.Split(data.Text, " ")

	switch dataSlice[0] {
	case "jm":
		num, err := strconv.ParseInt(dataSlice[1], 10, 64)
		if err != nil {
			zaplog.Logger.Warnf("Arg error: %v", err)
			_ = SendGroupMsg(client, global.ErrCmdArgFault)
			return err
		}
		if err = cmd.CmdJm(num); err != nil {
			zaplog.Logger.Fatalf("CmdJm error: %v", err)
			_ = SendGroupMsg(client, global.ErrCmdJmUnknownFault)
			return err
		}
	default:
		if err = SendGroupMsg(client, global.ErrCmdNotFound); err != nil {
			return err
		}
	}
	return nil
}
