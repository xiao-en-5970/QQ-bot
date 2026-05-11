// display_sync.go —— "顺带把旗下号展示信息同步到 hfut" 的轻量旁路。
//
// 背景：
//
//   bot 之前只在用户**触发业务动作**（上架 / 求购 / 提问等）时调
//   BotUpsertQQChild 上报 nickname / qq_avatar_url。问题是用户只在群里聊天、
//   没碰过业务动作时——旗下号要么还没创建，要么 nickname 是 NULL，app 端展示
//   就只能 fallback 到 username（qq+号），用户看不到他真实昵称。
//
// 设计：
//
//   - 每条群消息进 autoReplyManager.Push 时 fire-and-forget 调一次 UpsertQQChild
//   - 节流：同 user_id 30 分钟内只发一次，避免高频写库 + 浪费 hfut API quota
//   - 错误吞掉只 debug log——展示同步失败不应阻塞主消息处理
//   - 群没配学校（ErrGroupNoSchool）也直接吞掉——这种群本来 hfut 就不会落库，
//     不算业务异常
//
// 副作用：用户首次发任何群消息（即便不上架）就会被创建为旗下号——这符合 P2b
// "bot 在群里第一次见到这个 QQ 用户就建档"的语义；后续业务动作可直接复用。
package logic

import (
	"context"
	"sync"
	"time"

	"qq_bot/global"
	"qq_bot/utils/hfut"
	zaplog "qq_bot/utils/zap"
)

// displaySyncCooldown 同一 user_id 两次 sync 之间的最小间隔。
//
// 30 分钟是经验值：群名片改动频率很低；30 分钟够把绝大多数老旗下号 nickname
// 一两轮聊天周期内回填完。如果调得太短会浪费 hfut quota，太长则老数据回填慢。
const displaySyncCooldown = 30 * time.Minute

// displaySyncLastAt 内存节流表：user_id → 上次 sync 时间。
//
// 不持久化——bot 重启就重置，最坏情况是重启后第一波消息每个用户重新触发 sync。
// 100K 用户量级下也只占用几 MB，比起 Redis / DB 维护更简单。
var displaySyncLastAt sync.Map

// maybeSyncQQDisplay 节流后 fire-and-forget 调 UpsertQQChild 同步展示信息。
//
// 调用方应当在每条群消息入口直接 go maybeSyncQQDisplay(...) 即可——
// 内部已经 spawn goroutine + recover panic，不会阻塞调用方。
func maybeSyncQQDisplay(groupID, userID int64, userCard string) {
	if global.Hfut == nil || userID <= 0 || groupID == 0 {
		return
	}
	now := time.Now()
	if last, ok := displaySyncLastAt.Load(userID); ok {
		if t, _ := last.(time.Time); now.Sub(t) < displaySyncCooldown {
			return
		}
	}
	displaySyncLastAt.Store(userID, now)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				zaplog.Logger.Errorf("maybeSyncQQDisplay panic group=%d user=%d: %v", groupID, userID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := global.Hfut.UpsertQQChild(ctx, qqNumberOf(userID), groupID, userCard, qqAvatarURL(userID))
		switch {
		case err == nil:
			// 静默 OK
		case isExpectedDisplaySyncErr(err):
			zaplog.Logger.Debugf("maybeSyncQQDisplay 跳过 group=%d user=%d: %v", groupID, userID, err)
		default:
			zaplog.Logger.Debugf("maybeSyncQQDisplay 失败 group=%d user=%d: %v", groupID, userID, err)
		}
	}()
}

// isExpectedDisplaySyncErr 判断错误是否属于"可以静默吞掉" 类——
// 群没配学校、bot 服务暂未启用等都不算异常。
func isExpectedDisplaySyncErr(err error) bool {
	if err == nil {
		return false
	}
	if err == hfut.ErrGroupNoSchool {
		return true
	}
	return false
}
