package cmd

import (
	"context"
	"net/http"
	"qq_bot/global"
	zaplog "qq_bot/utils/zap"
	"strings"
	"time"
)

var (
	client = &http.Client{
		Timeout: time.Duration(120) * time.Second,
	}
)

// 解析指令
func ParseCmd(ctx context.Context) {
	var err error
	zaplog.Logger.Infof("协程ParseCmd启动")
	defer zaplog.Logger.Infof("协程ParseCmd退出")
	defer global.Wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case chanParseCmd, ok := <-global.ChanToParseCmd:
			if ok {
				zaplog.Logger.Debugf("<- global.ChanToJm，%#v", len(global.ChanToParseCmd))
				chanParseCmd.Data.Text = strings.TrimSpace(chanParseCmd.Data.Text)
				dataSlice := strings.Split(chanParseCmd.Data.Text, " ")
				chapter := int64(1)
				switch dataSlice[0] {
				case "jm":
					err = CmdJm(client, dataSlice, chanParseCmd.GroupID, chanParseCmd.UserID, chapter)
				case "github":
					err = CmdGithub(client, chanParseCmd.GroupID, chanParseCmd.UserID)
				case "help":
					err = CmdHelp(client, chanParseCmd.GroupID, chanParseCmd.UserID)
				default:
					err = CmdDefault(client, chanParseCmd.GroupID, chanParseCmd.UserID)
				}
				if err != nil {
					zaplog.Logger.Error(err)
				}

			}

		}
	}

}
