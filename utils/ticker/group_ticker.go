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

// GroupTicker 每隔 duration 调一次 GetNewAtMessage 拉群消息。
//
// seq 初值 0 = "还没成功初始化过"。
// 只要初始化成功一次（seq 变非零），之后即使 NapCat 偶发返回错/空也不会再重置 seq，
// 所以 `seq == 0` 只发生在持续初始化失败的群（NapCat 报"消息undefined不存在"等），
// 这种群我们重试 retry 次后认为不可用，退出本协程并把 ActiveGroups 标 false，
// 后续 UpdateGroupListTicker 会再尝试拉起来。
func GroupTicker(duration time.Duration, ctx context.Context, maxCount int, client *http.Client, group_id int64, retry int64) {
	global.Wg.Add(1)
	ticker := time.NewTicker(duration)
	defer ticker.Stop()
	defer global.Wg.Done()
	zaplog.Logger.Debugf("协程GroupTicker(GroupID:%d)启动", group_id)
	defer zaplog.Logger.Debugf("协程GroupTicker(GroupID:%d)退出", group_id)

	tickerChan := ticker.C

	count := 0
	var seq int64 = 0
	for {
		select {
		case <-tickerChan:
			count++
			err := logic.GetNewAtMessage(client, group_id, &seq)
			if seq == 0 {
				if retry >= conf.Cfg.Group.Retry {
					zaplog.Logger.Warnf("群初始化连续失败 %d 次，停止该群聊服务. group=%d", retry, group_id)
					global.ActiveGroups[group_id] = false
					return
				}
				retry++
				continue
			}
			if err != nil {
				zaplog.Logger.Error(err)
			}
			if count == maxCount {
				zaplog.Logger.Infof("已达到最大执行次数，退出程序。")
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
