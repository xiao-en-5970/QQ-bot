package ticker

import (
	"context"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	zaplog "qq_bot/utils/zap"
	"time"
)

func UpdateGroupListTicker(duration time.Duration, ctx context.Context, maxCount int, client *http.Client) {
	ticker := time.NewTicker(duration)
	defer ticker.Stop() // 确保在程序结束时停止 Ticker
	defer global.Wg.Done()
	zaplog.Logger.Infof("协程UpdateGroupListTicker启动")
	defer zaplog.Logger.Infof("协程UpdateGroupListTicker退出")
	// 使用一个通道来接收 Ticker 触发的事件
	tickerChan := ticker.C
	// 使用一个计数器来限制任务执行的次数（可选）
	count := 0
	for {
		select {
		case <-tickerChan:
			count++
			err, resp := logic.GetGroupList(client, true)
			if err != nil {
				zaplog.Logger.Error(err)
			}
			zaplog.Logger.Infof("群列表已更新")
			conf.Cfg.Group.GroupID = resp
			//发送信号表示开始更新群列表
			global.ChanToUpdateGroupList <- struct{}{}
			// 如果达到最大执行次数，退出循环
			if count == maxCount {
				zaplog.Logger.Infof("已达到最大执行次数，退出程序。")
				return
			}
		case <-ctx.Done():
			return

		}
	}
}
