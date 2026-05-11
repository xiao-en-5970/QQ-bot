// dup_off_shelf_state.go 维护「重复商品反问 → 用户做选择」这条链路的一次性上下文。
//
// 触发：用户上架时 hfut 后端 title 严格相等查重命中，bot 在群里反问：
//
//	「X」已发过（id=N）。回复 1=重复上架 / 2=下架旧的并上架
//
// 后续用户在 dupOffShelfTTL 内单独回 1 / 2 / "重复上架" / "下架旧的"，bot 立刻消费这个
// 上下文（不再走 Kimi 识别）：
//
//   - 1 / 重复上架：调 PublishGood with Force=true——旧的保留，新的创建
//   - 2 / 下架旧的：先 OffShelfGood 旧的，再 PublishGood 新的（不带 Force）
//   - 其它回复 / TTL 超时：忽略——默认不上架（用户的"判重严格了已经"的兜底语义）
//
// 状态里保存"原始上架请求 OriginalReq"，1 和 2 两条路径都要重新发起 publish 时
// 用到。
package logic

import (
	"strings"
	"sync"
	"time"

	"qq_bot/utils/hfut"
	zaplog "qq_bot/utils/zap"
)

// dupOffShelfTTL 比对消歧更长：用户可能读完提示、切 app 想一下再回。
const dupOffShelfTTL = 3 * time.Minute

// dupChoice 是用户对"重复商品反问"的选项枚举。
//
// 详见 matchesDupOffShelfReply：返回 0 = 不匹配；其它为用户选择。
type dupChoice int

const (
	dupChoiceNone       dupChoice = 0 // 用户的回复跟反问无关
	dupChoiceRepublish  dupChoice = 1 // "1" / "重复上架"——保留旧的，强制创建新的
	dupChoiceOffShelfOld dupChoice = 2 // "2" / "下架旧的"——下架旧的，再创建新的
)

// dupOffShelfPending 单次去重反问要带的数据。
//
// HfutUserID / GoodID / Title：hfut DuplicateGoodInfo 返回值；分别给"下架旧的"和
// "向用户回执"用。
//
// OriginalReq：用户原本要上架的完整 PublishGoodReq——选择 1（重复上架）或
// 2（下架旧的再上架）时都要用它重发起 publish。
//
// 同时也是 dispatch 层"重新上架成功后的 ack 文案" 所需信息的载体。
type dupOffShelfPending struct {
	HfutUserID  uint
	GoodID      uint
	Title       string
	OriginalReq *hfut.PublishGoodReq // 给 force-republish / off-shelf-then-republish 路径用
	CreatedAt   time.Time
}

type dupOffShelfManager struct {
	mu     sync.Mutex
	states map[autoReplyBucketKey]*dupOffShelfPending
}

var dupOffShelfMgr = &dupOffShelfManager{states: make(map[autoReplyBucketKey]*dupOffShelfPending)}

// Save 记下一次"重复商品反问"的上下文。后续用户的选择（1 / 2）走 Take。
func (m *dupOffShelfManager) Save(key autoReplyBucketKey, userID uint, goodID uint, title string, originalReq *hfut.PublishGoodReq) {
	p := &dupOffShelfPending{
		HfutUserID:  userID,
		GoodID:      goodID,
		Title:       title,
		OriginalReq: originalReq,
		CreatedAt:   time.Now(),
	}
	m.mu.Lock()
	m.states[key] = p
	m.mu.Unlock()
	zaplog.Logger.Debugf("dupOffShelf saved: group=%d qq=%d good=%d title=%q",
		key.GroupID, key.UserID, goodID, title)
}

// Peek 只读，不删——给 Push 判定"要不要立刻 flush"。
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

// Take 取出并删除；消费用户的选择（1 / 2）后调用，避免重复处理同一选择。
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

// matchesDupOffShelfReply 判断这段文字是不是"重复商品反问"的有效选择回复。
//
// 返回 dupChoice：
//
//   - dupChoiceNone        不匹配（用户回别的话，bot 不消费反问上下文，让它自然 TTL）
//   - dupChoiceRepublish   "1" / "重复上架" / "重复" / "再发"
//   - dupChoiceOffShelfOld "2" / "下架旧的" / "下架" / "替换"
func matchesDupOffShelfReply(flat string) dupChoice {
	t := stringsTrimDupCmd(flat)
	switch t {
	case "1":
		return dupChoiceRepublish
	case "2":
		return dupChoiceOffShelfOld
	}
	// 关键词兜底（用户可能不看选项编号而直接说选择）
	if strings.Contains(t, "重复上架") || strings.Contains(t, "再发一份") || strings.Contains(t, "再发一遍") {
		return dupChoiceRepublish
	}
	if strings.Contains(t, "下架旧的") || strings.Contains(t, "替换旧的") || strings.Contains(t, "下架旧") {
		return dupChoiceOffShelfOld
	}
	return dupChoiceNone
}

// stringsTrimDupCmd 去掉空白，方便"下 架 旧 的"也能命中。
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
