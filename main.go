package main

import (
	"context"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/cmdline"
	"qq_bot/utils/jm"
	"qq_bot/utils/ticker"
	"qq_bot/utils/wait_exit"
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
	// 创建客户端
	client := &http.Client{}
	//初始化日志
	zaplog.Init()
	//初始化viper读数据
	conf.Init()

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
		conf.Cfg.GroupID = append(conf.Cfg.GroupID, groupid)
	}

	if err != nil {
		panic(err)
	}
	zaplog.Logger.Infof("userid get succcess! userid: %d", *conf.Cfg.UserID)
	//获取ctx
	ctx, cancel := context.WithCancel(context.Background())

	global.Wg.Add(2)
	global.Wg.Add(len(conf.Cfg.GroupID))
	go wait_exit.WaitExit(cancel)
	go jm.Jmcomic(ctx)
	for _, groupID := range conf.Cfg.GroupID {
		go ticker.Ticker(10*time.Second, ctx, -1, client, groupID)
	}
	global.Wg.Wait()
	defer close(global.ChanToJm)
	defer client.CloseIdleConnections()
	defer zaplog.Logger.Sync()
	defer zaplog.LogFile.Close()
}
