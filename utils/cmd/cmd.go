package cmd

import (
	"context"
	"net/http"
	"qq_bot/global"
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
	chapter := int64(1)
	var err error
	switch dataSlice[0] {
	case "jm":
		err = CmdJm(client, dataSlice, chanParseCmd.GroupID, chanParseCmd.UserID, chapter)
	case "github":
		err = CmdGithub(client, chanParseCmd.GroupID, chanParseCmd.UserID)
	case "help":
		err = CmdHelp(client, dataSlice, chanParseCmd.GroupID, chanParseCmd.UserID)
	case "pix":
		err = CmdPix(client, dataSlice, chanParseCmd.GroupID)
	default:
		err = CmdDefault(client, chanParseCmd.GroupID, chanParseCmd.UserID)
	}
	if err != nil {
		zaplog.Logger.Error(err)
	}
}
