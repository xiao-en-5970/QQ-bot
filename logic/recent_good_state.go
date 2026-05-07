// recent_good_state.go 维护"用户最近一次成功上架的商品"——
// 给「不要了 / 不卖了 / 不出了」等"标准化撤销" 找回上下文用。
//
// 行为：
//
//	publish_good (cat=1/2) / seek_goods 成功 → recentGoodMgr.Save(key, …)
//	dispatchOffShelf 收到 hint="" 时优先查这里——若 30 分钟内有最近发布的、且仍在用户名下，
//	直接 OffShelfGood(goodID) 不需要再 ListActiveGoods。
//	相比"始终 ListActiveGoods 然后猜"，这条路径更精准（cat=1 / cat=2 都能命中），
//	且让 ack 文案能直接报"已下架你刚发的「鞋架」"。
//
// 30 分钟比 dupOffShelfTTL(3min) 长——用户可能发完商品聊一会再决定撤——但又比 ListActiveGoods
// 的"全部在售"短，确保只对"刚才那一条" 起作用。
package logic

import (
	"sync"
	"time"

	zaplog "qq_bot/utils/zap"
)

const recentGoodTTL = 30 * time.Minute

// recentGood 记一条：用户最近成功上架的商品 + hfut user_id（用于 OffShelfGood 鉴权）。
//
// Category=1 二手 / Category=2 求物品；区分仅用于 ack 文案（"已下架二手「X」" vs
// "已撤销求物品「X」"），下架接口本身按 goods.id 一视同仁。
type recentGood struct {
	HfutUserID uint
	GoodID     uint
	Title      string
	Category   int
	CreatedAt  time.Time
}

type recentGoodManager struct {
	mu     sync.Mutex
	states map[autoReplyBucketKey]*recentGood
}

var recentGoodMgr = &recentGoodManager{states: make(map[autoReplyBucketKey]*recentGood)}

// Save 记录一次成功上架——publish_good / seek_goods dispatch 在 hfut 接口成功后立刻调用。
//
// 同一 (group, user) 多次上架时**覆盖** 旧值（用户的"不要了"自然指向最近一条）。
func (m *recentGoodManager) Save(key autoReplyBucketKey, userID uint, goodID uint, title string, category int) {
	if goodID == 0 {
		return // hfut 偶发返 0 时跳过——后续仍能走 ListActiveGoods 兜底
	}
	r := &recentGood{
		HfutUserID: userID,
		GoodID:     goodID,
		Title:      title,
		Category:   category,
		CreatedAt:  time.Now(),
	}
	m.mu.Lock()
	m.states[key] = r
	m.mu.Unlock()
	zaplog.Logger.Debugf("recentGood saved: group=%d qq=%d good=%d title=%q cat=%d",
		key.GroupID, key.UserID, goodID, title, category)
}

// Peek 只读——dispatchOffShelf 在 hint="" 时调用。
//
// 过期会顺便清理，避免内存泄漏。
func (m *recentGoodManager) Peek(key autoReplyBucketKey) *recentGood {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.states[key]
	if !ok {
		return nil
	}
	if time.Since(r.CreatedAt) > recentGoodTTL {
		delete(m.states, key)
		return nil
	}
	return r
}

// Take 取出并删除——下架成功后调一下，避免用户后续"不要了"再次错杀。
func (m *recentGoodManager) Take(key autoReplyBucketKey) *recentGood {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.states[key]
	if !ok {
		return nil
	}
	delete(m.states, key)
	if time.Since(r.CreatedAt) > recentGoodTTL {
		return nil
	}
	return r
}
