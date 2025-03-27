package ticker

import (
	"net/http"
	"qq_bot/logic"
	zaplog "qq_bot/utils/zap"
	"time"
)

func Ticker(durration time.Duration, maxCount int, client *http.Client, seq int64) (err error) {
	ticker := time.NewTicker(durration)
	defer ticker.Stop() // 确保在程序结束时停止 Ticker

	// 使用一个通道来接收 Ticker 触发的事件
	tickerChan := ticker.C

	// 使用一个计数器来限制任务执行的次数（可选）
	count := 0

	// 使用一个无限循环来监听 Ticker 的事件
	for {
		select {
		case <-tickerChan:
			count++
			zaplog.Logger.Infof("Ticker 触发第 %d 次任务，当前时间：%s", count, time.Now().Format(time.RFC1123))
			err, seq = logic.GetNewAtMessage(client, seq)
			if err != nil {
				zaplog.Logger.Error(err)
				return
			}
			// 如果达到最大执行次数，退出循环
			if count >= maxCount {
				zaplog.Logger.Infof("已达到最大执行次数，退出程序。")
				return
			}
		}
	}
}
