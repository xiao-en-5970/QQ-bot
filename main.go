package main

import (
	"context"
	"net/http"
	_ "net/http/pprof"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/client_pool"
	"qq_bot/utils/cmd"
	"qq_bot/utils/cmdline"
	"qq_bot/utils/kimi"
	"qq_bot/utils/ticker"
	zaplog "qq_bot/utils/zap"
	"time"
)

var (
	err     error
	userid  int64
	groupid int64
)

func main() {

	ctx, cancel := context.WithCancel(context.Background())
	err = conf.Init()
	if err != nil {
		return
	}
	zaplog.Init()
	zaplog.Logger.Infof("配置读取成功, NapCat HTTP=%s", conf.Cfg.Server.Address)

	client := client_pool.NewClientPool()

	if err = logic.CheckNapCatAlive(client); err != nil {
		zaplog.Logger.Errorf("NapCat 连通性检查失败: %v", err)
		zaplog.Logger.Warnf("继续启动并等待 NapCat 恢复，期间相关 API 调用会失败")
	}

	// 初始化 Kimi（GPT_API_KEY 为空时返回 (nil, nil)，CmdDefault 自动回退到打印菜单）
	if global.Kimi, err = kimi.InitKimi(); err != nil {
		zaplog.Logger.Errorf("Kimi 初始化失败: %v，CmdDefault 将回退到打印菜单", err)
		global.Kimi = nil
	}

	if conf.Cfg.User.UserID == nil {
		if err, userid = logic.GetUserId(client); err != nil {
			zaplog.Logger.Panicf("用户id获取失败! err=%v", err)
			panic(err)
		} else {
			conf.Cfg.User.UserID = &userid
			zaplog.Logger.Infof("用户id获取成功! Bot id: %d", *conf.Cfg.User.UserID)
		}
		conf.Cfg.User.UserID = &userid
	}
	if conf.Cfg.Group.GroupID == nil {
		err, groupid = cmdline.GetCmdLine()
		if err != nil {
			zaplog.Logger.Warnf("未能从命令行找到群号，即将从账号中自动搜索 , err:%v", err)
		} else {
			conf.Cfg.Group.GroupID = append(conf.Cfg.Group.GroupID, groupid)
		}
	}
	if conf.Cfg.Group.GroupID == nil {
		err, conf.Cfg.Group.GroupID = logic.GetGroupList(client, true)
		if err != nil {
			zaplog.Logger.Panicf("群列表获取失败!")
			panic(err)
		} else {
			for _, id := range conf.Cfg.Group.GroupID {
				global.ActiveGroups[id] = true
			}
			zaplog.Logger.Infof("群列表获取成功! %#v", conf.Cfg.Group.GroupID)
		}
	}

	go ticker.WaitExit(cancel)
	go cmd.ParseCmd(ctx)
	go ticker.ClearCacheTicker(ctx)

	time.Sleep(time.Second)
	for _, groupID := range conf.Cfg.Group.GroupID {
		go ticker.GroupTicker(time.Duration(conf.Cfg.Group.GetGroupHistoryInterval)*time.Second, ctx, -1, client_pool.NewClientPool(), groupID, 0)
	}
	time.Sleep(time.Second)
	go ticker.UpdateGroupListTicker(time.Duration(conf.Cfg.Group.UpdateGroupListInterval)*time.Second, ctx)
	go func() {
		zaplog.Logger.Debugf("协程\"net/http/pprof\"启动")
		defer zaplog.Logger.Debugf("协程\"net/http/pprof\"退出")
		zaplog.Logger.Infoln(http.ListenAndServe("localhost:6060", nil))
	}()
	global.Wg.Wait()
	defer close(global.ChanToParseCmd)
	defer global.ThreadPool.Release()
	defer zaplog.Logger.Sync()
	defer zaplog.LogFile.Close()
}
