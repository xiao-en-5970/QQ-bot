package main

import (
	"net/http"
	"qq_bot/conf"
	"qq_bot/logic"
	"qq_bot/utils/ticker"
	zaplog "qq_bot/utils/zap"
	"time"
)

var (
	err error
)

// 定义要发送的数据结构

func main() {
	//初始化日志
	zaplog.Init()
	//初始化viper读数据
	conf.Init()
	// 创建客户端
	client := &http.Client{}
	var userid int64
	if conf.Cfg.UserID == nil {
		if err, userid = logic.GetUserId(client); err != nil {
			panic(err)
			return
		}
		conf.Cfg.UserID = &userid
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
