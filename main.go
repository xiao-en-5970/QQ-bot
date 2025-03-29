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

	//初始化viper读数据
	conf.Init()
	//初始化日志
	zaplog.Init(conf.Cfg.StdOutLogLevel)
	//如果未指定用户名，则自动获取
	if conf.Cfg.UserID == nil {
		if err, userid = logic.GetUserId(client); err != nil {
			panic(err)
			return
		}
		conf.Cfg.UserID = &userid
	}
	//如果未指定群号，则从命令行中获取
	if conf.Cfg.GroupID == nil {
		err, groupid = cmdline.GetCmdLine()
		if err != nil {
			zaplog.Logger.Warnf("未能从命令行找到群号，即将从账号中自动搜索 , err:%v", err)
		} else {
			conf.Cfg.GroupID = append(conf.Cfg.GroupID, groupid)
		}
	}
	//如果命令行获取不到，则从账户中获取全部群号
	if conf.Cfg.GroupID == nil {
		err, conf.Cfg.GroupID = logic.GetGroupList(client, false)
		if err != nil {
			//获取群聊列表失败
			zaplog.Logger.Errorf("Cant find groupID list , err:%v", err)
			panic(err)
		}
	}
	zaplog.Logger.Infof("userid get succcess! userid: %d", *conf.Cfg.UserID)
	zaplog.Logger.Infof("groupid get success! %#v", conf.Cfg.GroupID)
	//获取ctx
	ctx, cancel := context.WithCancel(context.Background())

	global.Wg.Add(2)
	global.Wg.Add(len(conf.Cfg.GroupID))
	go wait_exit.WaitExit(cancel)
	go jm.Jmcomic(ctx)
	for _, groupID := range conf.Cfg.GroupID {
		go ticker.Ticker(3*time.Second, ctx, -1, &http.Client{}, groupID)
	}
	global.Wg.Wait()
	defer close(global.ChanToJm)
	defer client.CloseIdleConnections()
	defer zaplog.Logger.Sync()
	defer zaplog.LogFile.Close()
}
