// Package logic 的 auto_reply.go 实现群聊白名单"窗口聚合 + LLM 识别"。
//
// 触发流程（详见 skill/bot/SKILL.md 的"窗口聚合"段）：
//
//	NapCat WS event (group message) → handle_at_message::handleAutoReply
//	  → autoReplyMgr.Push(groupID, userID, msg)        非阻塞，仅入桶
//	  → 桶里只是把消息追加进去 + 更新 LastSeenAt
//	StartAutoReplyScanner goroutine (5s 间隔) 扫描所有桶：
//	  → 桶满（>= MaxWindowSize）or 沉默够久（now - LastSeenAt >= WindowSeconds） → flush
//	  → flush 时把整段消息丢给 Kimi 做"业务动作识别"
//	  → P0 阶段：识别到动作只 log + 群里 @ 用户回执"识别到 XXX，业务接通中"，不真调 hfut
//
// 设计要点：
//   - per-(groupID, userID) 一个独立桶 (severityKey)，互不干扰
//   - 桶在内存里，bot 重启会丢——按设计可接受（详见 skill）
//   - 写入桶用 mutex 保护，扫描走 mutex 持锁拷贝快照后释放，不长持锁调 Kimi
//   - flush 完桶就清空（不是删，避免 map key 频繁 churn；下次 Push 直接复用）
//   - bot 自己发的消息 / 已经处理过的消息 不该走到这里（HandleAtMessage 顶部已过滤）
package logic

import (
	"context"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/model"
	zaplog "qq_bot/utils/zap"
	"sync"
	"time"
)

// autoReplyMsg 单条入桶的群消息（消息体的最小必要快照）。
//
// 不直接存 *model.Message：
//  1. 防止外部修改影响内存
//  2. 我们关心的就是 user_id / time / segments / 群名片，纯数据结构更好序列化给 LLM
type autoReplyMsg struct {
	MessageID  int64
	UserID     int64
	UserCard   string // 群名片 / 昵称（取自 sender.card / sender.nickname）
	Time       time.Time
	Segments   []model.MessageSegment // 原 segments（含 image url、at、text 等）
	FlatText   string                 // 扁平化的文本表示（含 [图片] 等占位符），方便 log / 喂 LLM
}

// autoReplyBucket 单个 (group, user) 的滑动窗口。
//
// LastSeenAt：最后一条消息的入桶时间（不是消息本身的 time，因为 NapCat 发回来的 time
// 可能跟服务器墙钟有偏差；用入桶时间做 silence 判定更稳）。
type autoReplyBucket struct {
	GroupID    int64
	UserID     int64
	Msgs       []autoReplyMsg
	LastSeenAt time.Time
}

// autoReplyManager 全局的窗口管理器。
//
// 所有方法都是并发安全的——Push 来自 wsclient 协程，Scan 来自专用扫描协程。
type autoReplyManager struct {
	mu      sync.Mutex
	buckets map[autoReplyBucketKey]*autoReplyBucket
}

type autoReplyBucketKey struct {
	GroupID int64
	UserID  int64
}

// 单例：跨 wsclient / scanner 共用同一份桶状态。
var autoReplyMgr = &autoReplyManager{
	buckets: make(map[autoReplyBucketKey]*autoReplyBucket),
}

// Push 把一条消息追加到对应桶。来自 handle_at_message::handleAutoReply。
//
// 不会主动 flush——flush 由扫描协程按时间/容量触发。
func (m *autoReplyManager) Push(groupID, userID int64, userCard string, msg *model.Message) {
	if msg == nil {
		return
	}
	key := autoReplyBucketKey{GroupID: groupID, UserID: userID}
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.buckets[key]
	if !ok {
		b = &autoReplyBucket{GroupID: groupID, UserID: userID}
		m.buckets[key] = b
	}
	b.Msgs = append(b.Msgs, autoReplyMsg{
		MessageID: msg.MessageID,
		UserID:    userID,
		UserCard:  userCard,
		Time:      now,
		Segments:  msg.Message,
		FlatText:  flattenMessageText(msg),
	})
	b.LastSeenAt = now

	// 桶超大时主动 flush——异常情况下用户狂发 30 条不停顿，不能一直攒。
	// 注意：Flush 内部需要拿不到 mu，这里释放后再调（用 goroutine 异步）。
	maxSize := conf.Cfg.Group.AutoReplyMaxWindowSize
	if maxSize <= 0 {
		maxSize = 20
	}
	if len(b.Msgs) >= maxSize {
		// 单独抽出快照、清空桶、起 goroutine 处理；保持 Push 是 O(1) 不阻塞 wsclient
		snapshot := b.Msgs
		b.Msgs = nil
		go m.processSnapshot(key, snapshot)
	}
}

// scanOnce 扫描一遍所有桶，把已经"沉默够久"的桶 flush 出去。
//
// 持锁阶段只做"决策 + 拷贝快照 + 清空原桶"，不调 Kimi、不打网络 IO；
// 拿到快照之后释放锁再异步处理，避免长时间阻塞 Push。
func (m *autoReplyManager) scanOnce() {
	silenceSec := conf.Cfg.Group.AutoReplyWindowSeconds
	if silenceSec <= 0 {
		silenceSec = 60
	}
	threshold := time.Duration(silenceSec) * time.Second
	now := time.Now()

	type ready struct {
		key      autoReplyBucketKey
		snapshot []autoReplyMsg
	}
	var ready_ []ready

	m.mu.Lock()
	for k, b := range m.buckets {
		if len(b.Msgs) == 0 {
			continue
		}
		if now.Sub(b.LastSeenAt) < threshold {
			continue
		}
		ready_ = append(ready_, ready{key: k, snapshot: b.Msgs})
		b.Msgs = nil // 清空已 flush 的部分
	}
	m.mu.Unlock()

	for _, r := range ready_ {
		m.processSnapshot(r.key, r.snapshot)
	}
}

// processSnapshot P0 阶段的"识别 + 回执"逻辑。
//
// 当前实现极简：只 log 整段，不调 Kimi、不真上架——P0 的核心是先把窗口聚合机制跑通，
// 让我能在 log 里看到完整的"60s 内某用户在某群说了什么"段，验证窗口边界对不对、
// 拼图（图 + 文）是不是按预期合并的。
//
// 后面 P0 第二步会把 Kimi 识别接上来：调 ChatRecognize()，让模型返回结构化 JSON
// 描述识别到的业务动作（publish_good / off_shelf 等），结果继续只 log + 群里 @ 用户回执，
// 不真调 hfut。
//
// 之后 P1 把回执位置接到真正的 hfut 客户端。
func (m *autoReplyManager) processSnapshot(key autoReplyBucketKey, snap []autoReplyMsg) {
	if len(snap) == 0 {
		return
	}
	first := snap[0]
	last := snap[len(snap)-1]
	zaplog.Logger.Infof("autoReply flush group=%d user=%d card=%q msgs=%d duration=%s",
		key.GroupID, key.UserID, first.UserCard, len(snap), last.Time.Sub(first.Time))
	for i, m := range snap {
		zaplog.Logger.Infof("  [%d] msgid=%d %s | %s",
			i+1, m.MessageID, m.Time.Format("15:04:05"), truncateForLog(m.FlatText, 200))
	}
	// TODO(P0-step2): 调 kimi.ChatRecognize 拿结构化 JSON
	// TODO(P0-step3): 群里 @ 用户回一句"识别到 XXX，业务接通中"
	// TODO(P1): 真调 hfut bot API 上下架
}

// StartAutoReplyScanner 启动后台扫描协程。
//
// 调用约定：必须由 main 在 `go StartAutoReplyScanner(ctx)` 之前调用 global.Wg.Add(1)，
// 跟其它后台协程同源（避免之前那个 Wg.Wait race）。
//
// 实现：5s 一次 tick，每次扫一遍 buckets 决定 flush。
// 5s 这个周期相对于 60s 沉默窗口足够细，不会让 flush 多延迟太多。
func StartAutoReplyScanner(ctx context.Context, _ *http.Client) {
	defer global.Wg.Done()
	zaplog.Logger.Debugf("协程AutoReplyScanner启动")
	defer zaplog.Logger.Debugf("协程AutoReplyScanner退出")

	t := time.NewTicker(5 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			autoReplyMgr.scanOnce()
		}
	}
}
