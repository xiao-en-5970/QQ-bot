// dup_off_shelf_state.go 维护「去重提示 → 用户回「下架旧的」」这条链路的一次性上下文。
//
// publish_good 命中 hfut DuplicateGoodInfo 时在群里提示用户回「下架旧的」——须记住要下架的那条
// goods id；用户稍后单发一句「下架旧的」应立刻 offshelf并回「已下架」，而不是等 silence 窗口
// 再走 Kimi（否则体感像「说完没反应」）。
package logic

import (
	"strings"
	"sync"
	"time"

	zaplog "qq_bot/utils/zap"
)

// dupOffShelfTTL 比对消歧更长：用户可能读完提示、切 app 想一下再回。
const dupOffShelfTTL = 3 * time.Minute

// dupOffShelfPending 单次去重-followup 要带的数据。
//
// GoodID / Title：hfut DuplicateGoodInfo 返回值；下架失败时 ACK 要带标题。
// HfutUserID：当时 upsert 后的旗下账号——OffShelfGood 的 caller id。
type dupOffShelfPending struct {
	HfutUserID uint
	GoodID     uint
	Title      string
	CreatedAt  time.Time
}

type dupOffShelfManager struct {
	mu     sync.Mutex
	states map[autoReplyBucketKey]*dupOffShelfPending
}

var dupOffShelfMgr = &dupOffShelfManager{states: make(map[autoReplyBucketKey]*dupOffShelfPending)}

func (m *dupOffShelfManager) Save(key autoReplyBucketKey, userID uint, goodID uint, title string) {
	p := &dupOffShelfPending{
		HfutUserID: userID,
		GoodID:     goodID,
		Title:      title,
		CreatedAt:  time.Now(),
	}
	m.mu.Lock()
	m.states[key] = p
	m.mu.Unlock()
	zaplog.Logger.Debugf("dupOffShelf saved: group=%d qq=%d good=%d title=%q",
		key.GroupID, key.UserID, goodID, title)
}

// Peek 只读，不删——给 Push 判定「要不要立刻 flush」。
func (m *dupOffShelfManager) Peek(key autoReplyBucketKey) *dupOffShelfPending {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.states[key]
	if !ok {
		return nil
	}
	if time.Since(p.CreatedAt) > dupOffShelfTTL {
		delete(m.states, key)
		return nil
	}
	return p
}

// Take 取出并删除；消费「下架旧的」成功 / 失败都应 Take 掉，避免重复下架。
func (m *dupOffShelfManager) Take(key autoReplyBucketKey) *dupOffShelfPending {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.states[key]
	if !ok {
		return nil
	}
	delete(m.states, key)
	if time.Since(p.CreatedAt) > dupOffShelfTTL {
		return nil
	}
	return p
}

// matchesDupOffShelfReply 用户整句里是否表达「下架旧的那条（去重里提到的那件）」。
func matchesDupOffShelfReply(flat string) bool {
	t := stringsTrimDupCmd(flat)
	return strings.Contains(t, "下架旧的")
}

// stringsTrimDupCmd 去掉空白，方便「下 架 旧 的」也能命中。
func stringsTrimDupCmd(flat string) string {
	s := []rune(strings.TrimSpace(flat))
	var b []rune
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '　' {
			continue
		}
		b = append(b, r)
	}
	return string(b)
}
