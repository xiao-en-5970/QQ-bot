// qq_display_sync.go —— 后台 scheduler：定期把所有 QQ 旗下号的最新 QQ 昵称同步到 hfut。
//
// 任务剖面：
//
//   - 启动时跑一次（让重启后的旗下号们立即得到一次回填）
//   - 每 syncInterval 跑一次定时全量同步
//   - 每 forcePollInterval poll hfut 的 `/bot/qq-sync/pending` 接口；
//     如果发现 admin 通过管理后台按下了"立即同步"按钮（force_at 比上次新），立即触发一轮
//
// 每轮同步：
//
//   1) hfut.ListQQChildren 分页拉所有旗下号 user_id / qq_number / created_in_group_id
//   2) 对每个 qq_number 调 NapCat get_stranger_info 拿 QQ 全局昵称（no_cache=true）
//   3) 拼默认头像 URL（q.qlogo.cn/headimg_dl?dst_uin=...&spec=640）
//   4) 调 hfut UpsertQQChild 写回——hfut 端对已存在旗下号走 applyQQChildDisplayUpdate 仅更新展示字段
//   5) 控速：每条之间 perEntryDelay 间隔，避免打满 NapCat
//
// 错误处理：
//
//   - NapCat 错误（QQ 不存在 / 注销 / 网络抖）：保留库里原值，仅 debug log
//   - hfut 错误：debug log；下一轮重试
//   - 单条失败不影响整轮——继续遍历，最后输出"成功 N 失败 M"汇总
package logic

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"qq_bot/global"
	"qq_bot/model"
	"qq_bot/service"
	"qq_bot/utils/client_pool"
	"qq_bot/utils/hfut"
	zaplog "qq_bot/utils/zap"
)

const (
	// 全量同步周期——QQ 群名片 / 昵称改动频率不高，6 小时一次足够 + 节省 NapCat quota。
	syncInterval = 6 * time.Hour
	// 管理后台"立即同步"信号位 poll 周期。
	forcePollInterval = 30 * time.Second
	// 单条同步之间的间隔——避免一轮把 NapCat 打满。
	perEntryDelay = 100 * time.Millisecond
	// 单次 ListQQChildren 分页大小。
	listPageSize = 200
)

// syncRunning 全局守卫，避免定时 ticker 和 force poll 同时触发两次并发同步。
//
// 一轮全量在大库存下可能耗时数分钟（1000 旗下号 × 100ms = 100s）；
// 第二个触发到来时 syncRunning != 0 → 直接跳过。
var syncRunning int32

// StartQQDisplaySync 启动旗下号展示信息同步后台协程。
//
// ctx 取消时协程退出；不阻塞调用方。调用方应当先 global.Wg.Add(1)。
func StartQQDisplaySync(ctx context.Context) {
	go run(ctx)
}

func run(ctx context.Context) {
	defer global.Wg.Done()
	zaplog.Logger.Infof("scheduler: qq display sync started (every %s + force poll %s)",
		syncInterval, forcePollInterval)
	defer zaplog.Logger.Infof("scheduler: qq display sync stopped")

	// 启动时跑一次 + 给一点缓冲让 hfut / NapCat 都就绪。
	// 不用 time.AfterFunc 是因为 ctx 取消时它的回调可能还在等——直接在主 loop 处理更可控。
	startupTimer := time.NewTimer(30 * time.Second)
	defer startupTimer.Stop()

	var lastSeenForceAt int64

	syncTicker := time.NewTicker(syncInterval)
	defer syncTicker.Stop()
	forceTicker := time.NewTicker(forcePollInterval)
	defer forceTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-startupTimer.C:
			runOnce(ctx, "startup")
		case <-syncTicker.C:
			runOnce(ctx, "scheduled")
		case <-forceTicker.C:
			if global.Hfut == nil {
				continue
			}
			pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			at, err := global.Hfut.GetQQSyncForceAt(pollCtx)
			cancel()
			if err != nil {
				zaplog.Logger.Debugf("scheduler: qq sync force-poll 失败: %v", err)
				continue
			}
			if at > 0 && at != lastSeenForceAt {
				lastSeenForceAt = at
				zaplog.Logger.Infof("scheduler: qq display sync 收到 admin 立即同步信号 (force_at=%d)", at)
				go runOnce(ctx, "admin")
			}
		}
	}
}

// runOnce 跑一轮完整同步。trigger 仅作 log 标记（startup / scheduled / admin）。
func runOnce(parent context.Context, trigger string) {
	if !atomic.CompareAndSwapInt32(&syncRunning, 0, 1) {
		zaplog.Logger.Infof("scheduler: qq display sync 已有任务在跑，跳过本次 trigger=%s", trigger)
		return
	}
	defer atomic.StoreInt32(&syncRunning, 0)

	if global.Hfut == nil {
		zaplog.Logger.Debugf("scheduler: qq display sync 跳过 (hfut 客户端未初始化)")
		return
	}
	start := time.Now()
	zaplog.Logger.Infof("scheduler: qq display sync 开始 trigger=%s", trigger)

	napcat := client_pool.NewClientPool()
	cursor := uint(0)
	totalOK, totalSkip, totalFail := 0, 0, 0
	for {
		if parent.Err() != nil {
			zaplog.Logger.Infof("scheduler: qq display sync 被取消 trigger=%s", trigger)
			return
		}
		listCtx, cancel := context.WithTimeout(parent, 10*time.Second)
		resp, err := global.Hfut.ListQQChildren(listCtx, cursor, listPageSize)
		cancel()
		if err != nil {
			zaplog.Logger.Warnf("scheduler: qq display sync ListQQChildren 失败 cursor=%d: %v", cursor, err)
			break
		}
		for _, entry := range resp.List {
			ok, skip, fail := syncOneEntry(parent, napcat, entry)
			totalOK += ok
			totalSkip += skip
			totalFail += fail
			select {
			case <-parent.Done():
				return
			case <-time.After(perEntryDelay):
			}
		}
		if resp.NextCursor == 0 {
			break
		}
		cursor = resp.NextCursor
	}
	zaplog.Logger.Infof("scheduler: qq display sync 完成 trigger=%s 成功=%d 跳过=%d 失败=%d 耗时=%s",
		trigger, totalOK, totalSkip, totalFail, time.Since(start).Truncate(time.Millisecond))
}

// syncOneEntry 同步单个旗下号——返回 (ok, skip, fail) 计数。
//
//	ok    成功调 NapCat + 写回 hfut
//	skip  无 qq_number / NapCat 没返回有效 nickname / hfut 写库被跳过
//	fail  hfut UpsertQQChild 真的报错（quota / 网络）
func syncOneEntry(parent context.Context, napcat *http.Client, entry hfut.QQChildEntry) (ok, skip, fail int) {
	qqStr := strings.TrimSpace(entry.QQNumber)
	if qqStr == "" {
		return 0, 1, 0
	}
	qq, err := strconv.ParseInt(qqStr, 10, 64)
	if err != nil || qq <= 0 {
		zaplog.Logger.Debugf("scheduler: qq display sync 跳过 (无效 qq_number=%q user_id=%d)", qqStr, entry.UserID)
		return 0, 1, 0
	}

	// 拉 QQ 全局昵称
	napCtx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	_ = napCtx // BaseService 不带 ctx；保留是为了 future 重构 NapCat 客户端
	nickErr, resp := service.GetStrangerInfo(napcat, &model.GetStrangerInfoReq{
		UserID:  qq,
		NoCache: true,
	})
	nickname := ""
	if nickErr == nil && resp != nil {
		nickname = strings.TrimSpace(resp.Data.Nickname)
	}
	if nickname == "" {
		// NapCat 没拿到——不动现有 nickname。但头像 URL 依然推送一次（让历史空头像被回填）
		if entry.QQAvatarURL == "" {
			avatar := defaultQQAvatarURL(qqStr)
			_, uerr := global.Hfut.UpsertQQChild(parent, qqStr, entry.CreatedInGroupID, "", avatar)
			if uerr != nil {
				return 0, 0, 1
			}
			return 1, 0, 0
		}
		return 0, 1, 0
	}

	// 写回 hfut——nickname 和头像 URL 一起推
	avatar := entry.QQAvatarURL
	if avatar == "" {
		avatar = defaultQQAvatarURL(qqStr)
	}
	upsertCtx, cancel2 := context.WithTimeout(parent, 10*time.Second)
	defer cancel2()
	_, uerr := global.Hfut.UpsertQQChild(upsertCtx, qqStr, entry.CreatedInGroupID, nickname, avatar)
	if uerr != nil {
		zaplog.Logger.Debugf("scheduler: qq display sync UpsertQQChild 失败 user_id=%d qq=%s: %v",
			entry.UserID, qqStr, uerr)
		return 0, 0, 1
	}
	return 1, 0, 0
}

// defaultQQAvatarURL 拼出腾讯永久头像 CDN URL——跟 hfut 端 service.defaultQQAvatarURL 保持一致。
func defaultQQAvatarURL(qq string) string {
	qq = strings.TrimSpace(qq)
	if qq == "" {
		return ""
	}
	return "https://q.qlogo.cn/headimg_dl?dst_uin=" + qq + "&spec=640"
}
