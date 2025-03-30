package ticker

import (
	"context"
	"fmt"
	"net/http"
	"qq_bot/global"
	"qq_bot/logic"
	zaplog "qq_bot/utils/zap"
	"time"
)

func GroupTicker(duration time.Duration, ctx context.Context, maxCount int, client *http.Client, group_id int64) {
	ticker := time.NewTicker(duration)
	defer ticker.Stop() // 确保在程序结束时停止 Ticker
	defer global.Wg.Done()
	zaplog.Logger.Debugf("协程GroupTicker(GroupID:%d)启动", group_id)
	defer zaplog.Logger.Debugf("协程GroupTicker(GroupID:%d)退出", group_id)

	// 使用一个通道来接收 Ticker 触发的事件
	tickerChan := ticker.C

	// 使用一个计数器来限制任务执行的次数（可选）
	count := 0
	var seq int64 = 0
	// 使用一个无限循环来监听 Ticker 的事件
	for {
		select {
		case <-tickerChan:
			count++
			err := logic.GetNewAtMessage(client, group_id, &seq)
			if seq == 0 {
				zaplog.Logger.Warnf(fmt.Sprintf("该群聊消息数量不足，停止该群聊服务.%#v", group_id))
				return
			}
			if err != nil {
				zaplog.Logger.Error(err)
			}
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
