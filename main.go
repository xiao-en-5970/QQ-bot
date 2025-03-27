package main

import (
	"net/http"
	"qq_bot/conf"
	"qq_bot/logic"
	"qq_bot/utils/cmdline"
	"qq_bot/utils/ticker"
	zaplog "qq_bot/utils/zap"
	"time"
)

var (
	err     error
	userid  int64
	groupid int64
)

// 定义要发送的数据结构

func main() {

	//初始化日志
	zaplog.Init()
	//初始化viper读数据
	conf.Init()

	// 创建客户端
	client := &http.Client{}

	if conf.Cfg.UserID == nil {
		if err, userid = logic.GetUserId(client); err != nil {
			panic(err)
			return
		}
		conf.Cfg.UserID = &userid
	}
	if conf.Cfg.GroupID == nil {
		err, groupid = cmdline.GetCmdLine()
		if err != nil {
			zaplog.Logger.Fatalf("Cant find groupID , err:%v", err)
			panic(err)
		}
		conf.Cfg.GroupID = &groupid
	}

	if err != nil {
		panic(err)
	}
	zaplog.Logger.Infof("userid get succcess! userid: %d", *conf.Cfg.UserID)

	ticker.Ticker(5*time.Second, 10, client, 0)

	defer client.CloseIdleConnections()

	defer zaplog.Logger.Sync()
	defer zaplog.LogFile.Close()
}
