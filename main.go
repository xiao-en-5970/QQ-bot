package main

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"qq_bot/conf"
	"qq_bot/global"
	internalapi "qq_bot/internal/api"
	"qq_bot/logic"
	"qq_bot/utils/client_pool"
	"qq_bot/utils/cmd"
	"qq_bot/utils/cmdline"
	"qq_bot/utils/hfut"
	"qq_bot/utils/kimi"
	"qq_bot/utils/ticker"
	"qq_bot/utils/wsclient"
	zaplog "qq_bot/utils/zap"
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
	// 在 zap 初始化之前用 fmt 直接打到 stdout，确认 env / yaml 解析后实际拿到的 log level，
	// 否则一旦 level 落到 warn（默认值），bot 启动时的 info / debug 全都看不见，没法排查。
	fmt.Printf("[boot] log.std_out_log_level=%q  log.log_level=%q  log_file=%q\n",
		conf.Cfg.Log.StdOutLogLevel, conf.Cfg.Log.LogLevel, conf.Cfg.Log.LogFile)
	zaplog.Init()
	zaplog.Logger.Infof("配置读取成功, NapCat HTTP=%s", conf.Cfg.Server.Address)

	// 严格 opt-in：commands.enabled 为空时所有 @bot 都会被静默忽略。
	// 启动期就 WARN 一下，避免管理员"为啥 bot 不响应"困惑半天。
	if !conf.Cfg.Commands.AnyEnabled() {
		zaplog.Logger.Warnf("commands.enabled 未配置或为空 → bot 启动后将不响应任何 @ 指令" +
			"。如果要启用，例如 env 设 COMMANDS_ENABLED=jm,pix,help,github,chat")
	} else {
		zaplog.Logger.Infof("已启用的命令: %v, default=%q",
			conf.Cfg.Commands.Enabled, conf.Cfg.Commands.Default)
	}

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

	// 初始化 hfut 客户端（auto_reply 路径会用）。
	// URL 或 JWT secret 任一缺失都不初始化——auto_reply 检测到 global.Hfut == nil 时回退到
	// "占位 ack"链路，方便先跑识别 demo 看效果再接 hfut。
	//
	// service-to-service 鉴权走"共享 secret + bot 自签 JWT"模式（详见 utils/hfut 包注释）：
	// QQ-bot env 配 HFUT_API_JWT_SECRET，hfut env 配同样的 secret，0 维护数据库 token。
	if conf.Cfg.Hfut.APIURL != "" && conf.Cfg.Hfut.APIJWTSecret != "" {
		hc, herr := hfut.NewClient(conf.Cfg.Hfut.APIURL, conf.Cfg.Hfut.APIJWTSecret, "qq-bot")
		if herr != nil {
			zaplog.Logger.Errorf("hfut 客户端初始化失败: %v；auto_reply 将回退到占位 ack", herr)
		} else {
			global.Hfut = hc
			zaplog.Logger.Infof("hfut 客户端已启用 (url=%s, 自签 JWT 模式)", conf.Cfg.Hfut.APIURL)
		}
	} else {
		zaplog.Logger.Warnf("HFUT_API_URL / HFUT_API_JWT_SECRET 未完整配置，auto_reply 不会真上架（识别仍然能跑，回执用 [识别测试] 占位）")
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
	// 群列表只用来打印一下"bot 在哪些群"方便排查；WS 模式下不再按群跑独立 ticker，
	// NapCat 会把 bot 加入的所有群的消息都通过同一条 WebSocket 推过来。
	if conf.Cfg.Group.GroupID == nil {
		if err, conf.Cfg.Group.GroupID = logic.GetGroupList(client, true); err != nil {
			zaplog.Logger.Warnf("群列表获取失败（不影响 WS 收消息）: %v", err)
		} else {
			zaplog.Logger.Infof("群列表获取成功! %#v", conf.Cfg.Group.GroupID)
		}
	}

	// 收尾顺序（LIFO）：log file → log sync → 池释放 → channel 关闭。
	// 这些放在 Wg.Wait 之前 defer，是为了即使 Wg.Wait 永远不返回（被信号 kill）也能注册上。
	defer zaplog.LogFile.Close()
	defer zaplog.Logger.Sync()
	defer global.ThreadPool.Release()
	defer close(global.ChanToParseCmd)

	// === 启动后台协程 =======================================================
	// Wg.Add(1) 必须由父协程在 `go ...` 之前调用，否则父协程可能"抢跑"到 Wg.Wait()
	// 时各 goroutine 还没来得及自己 Add，counter 仍是 0，Wait 立刻返回，
	// main 直接退出，bot 一启动就死。这是之前 time.Sleep 兜底掩盖的同一个 bug。
	const backgroundJobs = 8 // WaitExit, ParseCmd, ClearCacheTicker, wsclient.Run, AutoReplyScanner, InternalAPI, QQDisplaySync, RuntimeConfigSync
	for i := 0; i < backgroundJobs; i++ {
		global.Wg.Add(1)
	}
	go ticker.WaitExit(cancel)
	go cmd.ParseCmd(ctx)
	go ticker.ClearCacheTicker(ctx)
	// WebSocket 长连接，唯一的"消息入口"。断线指数退避自动重连，详见 wsclient.Run 注释。
	// 老的轮询方案（GroupTicker / UpdateGroupListTicker）已弃用，原因见 utils/wsclient 包注释。
	go wsclient.Run(ctx)
	// 群聊白名单的"窗口聚合"扫描协程：每 5s 扫一次所有 (group,user) 桶，沉默到时的就 flush。
	// 仅当 Group.AutoReplyWhitelist 非空时这个协程才会真有事干；空白名单时它每 5s 空转一次没影响。
	go logic.StartAutoReplyScanner(ctx, client_pool.NewClientPool())
	// hfut 反向调用接口（QQ 绑定 / 转发等）。
	// Internal.Token 为空时 Start() 立即 return，goroutine 也按 Wg.Done 正常释放。
	go internalapi.Start(ctx)
	// 旗下号展示信息（昵称 / 头像）后台定时同步——启动后 30s 跑一次，之后每 6h 跑一次；
	// 同时 30s 间隔 poll hfut 接收"管理后台立即同步"信号。详见 logic/qq_display_sync.go。
	logic.StartQQDisplaySync(ctx)
	// Bot 运行时配置同步：从 hfut /api/v1/bot/runtime-config 拉群白名单 / 运维群 / silent_mode，
	// 启动同步拉一次 + 之后每 60s poll；env / yaml 配的同名字段作为兜底。
	// 详见 logic/runtime_config_sync.go。HFUT_API_URL 未配时 StartRuntimeConfigSync 立即跳过，
	// 但 Wg.Done 仍要由它内部完成——这里加 wg 计数已经预留 1 个，让 Start 在不启 goroutine 时
	// 也消费掉它。
	logic.StartRuntimeConfigSync(ctx)

	// pprof 是 daemon 性质的调试入口，不参与 Wg（挂了也不该影响 bot 退出语义）。
	go func() {
		zaplog.Logger.Debugf("协程\"net/http/pprof\"启动")
		defer zaplog.Logger.Debugf("协程\"net/http/pprof\"退出")
		zaplog.Logger.Infoln(http.ListenAndServe("localhost:6060", nil))
	}()

	// 配置热更新：yaml 文件改动自动 reload；kill -HUP <pid> 强制 reload。
	// 详见 conf/conf.go 的 WatchAndReload 注释。这条 goroutine 不进 Wg——
	// 它由 ctx 控制退出，bot 关闭时随 ctx.Cancel 自然结束。
	go conf.WatchAndReload(ctx)

	zaplog.Logger.Infof("bot 启动完成，等待信号 / WS 事件...")
	global.Wg.Wait()
	zaplog.Logger.Infof("bot 全部协程已退出，main 返回")
}
