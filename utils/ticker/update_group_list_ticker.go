package ticker

import (
	"context"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/client_pool"
	zaplog "qq_bot/utils/zap"
	"time"
)

func UpdateGroupListTicker(duration time.Duration, ctx context.Context) {
	global.Wg.Add(1)
	defer global.Wg.Done()

	ticker := time.NewTicker(duration)
	defer ticker.Stop() // 确保在程序结束时停止 Ticker

	zaplog.Logger.Debugf("协程UpdateGroupListTicker启动")
	defer zaplog.Logger.Debugf("协程UpdateGroupListTicker退出")
	// 使用一个通道来接收 Ticker 触发的事件
	tickerChan := ticker.C
	// 使用一个计数器来限制任务执行的次数（可选）

	for {
		select {
		case <-tickerChan:
			err, resp := logic.GetGroupList(client_pool.NewClientPool(), true)
			if err != nil {
				zaplog.Logger.Error(err)
			}

			zaplog.Logger.Infof("群列表开始刷新，重新检测活跃群聊")
			conf.Cfg.Group.GroupID = resp
			for _, g := range conf.Cfg.Group.GroupID {
				if global.ActiveGroups[g] == false {

					go GroupTicker(time.Duration(conf.Cfg.Group.GetGroupHistoryInterval)*time.Second, ctx, -1, client_pool.NewClientPool(), g, 0)
					global.ActiveGroups[g] = true
				}
			}

			// 如果达到最大执行次数，退出循环
		case <-ctx.Done():
			return

		}
	}
}
