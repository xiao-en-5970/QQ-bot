package main

import (
	"context"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/cmd"
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
	// 创建客户端
	client := &http.Client{}
	//初始化上下文用于退出
	ctx, cancel := context.WithCancel(context.Background())
	//初始化viper读数据
	err = conf.Init()
	if err != nil {
		return
	}
	//初始化日志
	zaplog.Init()
	zaplog.Logger.Infof("配置读取成功")
	//如果未指定用户名，则自动获取
	if conf.Cfg.User.UserID == nil {
		if err, userid = logic.GetUserId(client); err != nil {
			zaplog.Logger.Panicf("用户id获取失败!")
			panic(err)
			return
		} else {
			conf.Cfg.User.UserID = &userid
			zaplog.Logger.Infof("用户id获取成功! userid: %d", *conf.Cfg.User.UserID)
		}
		conf.Cfg.User.UserID = &userid
	}
	//如果未指定群号，则从命令行中获取
	if conf.Cfg.Group.GroupID == nil {
		err, groupid = cmdline.GetCmdLine()
		if err != nil {
			zaplog.Logger.Warnf("未能从命令行找到群号，即将从账号中自动搜索 , err:%v", err)
		} else {
			conf.Cfg.Group.GroupID = append(conf.Cfg.Group.GroupID, groupid)
		}
	}
	//如果命令行获取不到，则从账户中获取全部群号
	if conf.Cfg.Group.GroupID == nil {
		err, conf.Cfg.Group.GroupID = logic.GetGroupList(client, true)
		if err != nil {
			zaplog.Logger.Panicf("群列表获取失败!")
			panic(err)
			return
		} else {
			zaplog.Logger.Infof("群列表获取成功! %#v", conf.Cfg.Group.GroupID)
		}
		//global.Wg.Add(1)
		//go ticker.UpdateGroupListTicker(time.Duration(conf.Cfg.Group.UpdateGroupListInterval)*time.Second, ctx, -1, &http.Client{})
	}
	zaplog.Logger.Infof("配置读取成功")
	global.Wg.Add(2)
	global.Wg.Add(len(conf.Cfg.Group.GroupID))
	//等待信号用于优雅退出并取消其他协程
	go ticker.WaitExit(cancel)
	// 开一个协程用于解析命令（其实是防止循环引用）
	go cmd.ParseCmd(ctx)
	//防止前两个协程没执行完
	time.Sleep(time.Second)
	// 每个群聊都开一个协程用于追踪群消息
	for _, groupID := range conf.Cfg.Group.GroupID {
		go ticker.GroupTicker(time.Duration(conf.Cfg.Group.GetGroupHistoryInterval)*time.Second, ctx, -1, &http.Client{}, groupID)
	}
	//等待协程退出
	global.Wg.Wait()
	defer close(global.ChanToParseCmd)
	defer client.CloseIdleConnections()
	defer zaplog.Logger.Sync()
	defer zaplog.LogFile.Close()
}
