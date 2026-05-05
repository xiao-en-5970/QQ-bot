// Package logic 的 disambig_state.go 维护两类 per-(group, user) 的内存短期状态：
//
//  1. 消歧上下文 (P3.2)：dispatch 检测到 candidates ≥ 2 且用户没指明哪条时，
//     存一份上下文（候选列表 + 原 action 类型），在群里 reply ①/②/③；
//     用户在 1 分钟内回 1/2/3/①②③ 等数字 → processSnapshot 跳过 Kimi 直接消费。
//
//  2. dispatch 限流 (P3.4)：滑动窗口计数器——同一 (group, user) 在 1 分钟内
//     publish_good / publish_question 等"会落库"的动作如果触发 ≥ 3 次，
//     第 4 次开始进入 cooldown，dispatch 入口直接拒绝并 ack 一条提示。
//
// 两者都在内存里、单进程，bot 重启会丢——跟 autoReplyMgr 的设计一致；
// 长效审计 / 跨实例共享后续再用 Redis（见 SKILL.md "P3.4 限流/审计"段）。
package logic

import (
	"strings"
	"sync"
	"time"

	zaplog "qq_bot/utils/zap"
)

// =============================================================================
// 消歧上下文（P3.2）
// =============================================================================

// disambigKind 表示消歧覆盖的"原 action 类型"——选定后回到对应分支处理。
type disambigKind int

const (
	disambigKindOffShelf      disambigKind = iota + 1 // 下架商品
	disambigKindCloseQuestion                         // 关闭提问
)

// disambigCandidate 单个候选项——既能放下架商品（id+title）也能放关闭提问，
// 字段命名取 ID/Title 这种通用语义。
type disambigCandidate struct {
	ID    uint
	Title string
}

// disambigContext 一个待消歧的状态。
type disambigContext struct {
	Kind       disambigKind
	UserID     uint                // hfut user_id（已 upsert 过）
	GroupID    int64               // 用于关闭提问时再 list_open_questions
	Candidates []disambigCandidate // 列表顺序对应 1 / ① ②
	CreatedAt  time.Time
}

// disambigManager 全局消歧状态管理器。
//
// 跟 autoReplyMgr 一样是单例，按 (group, user) 索引；TTL 由读取方按
// CreatedAt + disambigTTL 自行判断（懒过期，不起独立清理协程）。
type disambigManager struct {
	mu     sync.Mutex
	states map[autoReplyBucketKey]*disambigContext
}

// disambigTTL 消歧状态的有效期。
//
// 1 分钟：跟 autoReply 的 silence 窗口接近——用户大多在 5~60s 内回选择数字；
// 超过 1 分钟没回 = 用户走神 / 改话题，状态自然失效，避免乱串。
const disambigTTL = 60 * time.Second

var disambigMgr = &disambigManager{states: make(map[autoReplyBucketKey]*disambigContext)}

// Save 把一份消歧上下文存进去——后续来自同一 (group, user) 的"数字选择"消息
// 会消费它。
func (m *disambigManager) Save(key autoReplyBucketKey, c *disambigContext) {
	if c == nil {
		return
	}
	c.CreatedAt = time.Now()
	m.mu.Lock()
	m.states[key] = c
	m.mu.Unlock()
	zaplog.Logger.Debugf("disambig saved: group=%d user=%d kind=%d candidates=%d",
		key.GroupID, key.UserID, c.Kind, len(c.Candidates))
}

// Get 读取（不删除）一份消歧上下文——用于 Push 时判断是否进入"立刻 flush"路径。
//
// 若已过 TTL 或不存在，返回 nil；若 TTL 过期还会顺手删掉那条 stale 状态。
func (m *disambigManager) Get(key autoReplyBucketKey) *disambigContext {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.states[key]
	if !ok {
		return nil
	}
	if time.Since(c.CreatedAt) > disambigTTL {
		delete(m.states, key)
		return nil
	}
	return c
}

// Take 取走（取出 + 删除）一份消歧上下文，用于消费时——一次有效，命中即清。
func (m *disambigManager) Take(key autoReplyBucketKey) *disambigContext {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.states[key]
	if !ok {
		return nil
	}
	delete(m.states, key)
	if time.Since(c.CreatedAt) > disambigTTL {
		return nil
	}
	return c
}

// Clear 显式清空——用户在 TTL 内说了别的话题（不是数字选择），dispatch 检测到
// "有 disambig 状态但当前 action 不是消歧选择" 时主动清，避免污染下次。
func (m *disambigManager) Clear(key autoReplyBucketKey) {
	m.mu.Lock()
	delete(m.states, key)
	m.mu.Unlock()
}

// disambigChoiceFromText 从一条短消息文本里识别用户选了第几个候选——返回
// 1-based index。返回 0 表示这条消息不是"消歧选择"，调用方应当走正常识别。
//
// 接受的输入格式：
//   - 阿拉伯数字 "1" / "2" / "3"（前后允许空白 / 标点）
//   - 圆圈数字 "①" / "②" / "③"
//   - 其它无关文本（如"算了"、"嗯"）→ 0
//
// 故意把识别非常窄——避免误把"我要1元卖鞋架"这种正常消息当作消歧选择。
func disambigChoiceFromText(text string) int {
	t := strings.TrimSpace(text)
	if t == "" {
		return 0
	}
	// 去常见尾部标点
	t = strings.TrimRight(t, ".。！!?？")
	t = strings.TrimSpace(t)
	if len(t) > 4 { // "①" 是 3 字节，留点余量；超过 4 字节 ≈ 多字符不算
		return 0
	}
	switch t {
	case "1", "①":
		return 1
	case "2", "②":
		return 2
	case "3", "③":
		return 3
	case "4", "④":
		return 4
	case "5", "⑤":
		return 5
	}
	return 0
}

// =============================================================================
// dispatch 限流（P3.4）
// =============================================================================

// dispatchRateWindow 滑动窗口大小——1 分钟。
//
// 业务节奏：用户正常发布 ≤ 1 次 / 分钟；> 3 次大概率是误识别 / 滥用 / 漏配的
// 自动化脚本。窗口选 1min 既能抓住"短时刷屏"，又能让正常用户在跨分钟时不被
// 误锁——典型场景：用户上架完一件想再上架第二件，这种隔个十几秒、每分钟 1~2
// 次的节奏不会触发限流。
const dispatchRateWindow = 1 * time.Minute

// dispatchRateMaxPerWindow 同 (group, user) 在 dispatchRateWindow 内允许的
// "会落库"动作上限。第 N+1 次开始 dispatch 入口直接拒绝并返回 cooldown ack。
const dispatchRateMaxPerWindow = 3

// dispatchRateBucket 一个 (group, user) 的最近事件时间戳。
//
// 简单起见用 slice 存所有事件 timestamp；append 时顺手清掉过期的。
// 量级估计：单 user 单分钟最多攒 3 条（再多就被限流），slice 不会膨胀。
type dispatchRateBucket struct {
	timestamps []time.Time
}

// dispatchRateLimiter 全局限流器。
type dispatchRateLimiter struct {
	mu      sync.Mutex
	buckets map[autoReplyBucketKey]*dispatchRateBucket
}

var dispatchLimiter = &dispatchRateLimiter{buckets: make(map[autoReplyBucketKey]*dispatchRateBucket)}

// Allow 试图为 (group, user) 记一次"会落库"事件——返回 true 表示允许、并已经计数；
// false 表示触发限流，调用方应该返回 cooldown ack 而**不**调 hfut。
//
// 设计选择：把"判定 + 累加"合二为一，避免调用方两步操作之间出现 race
// （两条 goroutine 同时检查时都看到 < 阈值就都加进去）。
func (l *dispatchRateLimiter) Allow(key autoReplyBucketKey) (allowed bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &dispatchRateBucket{}
		l.buckets[key] = b
	}
	cutoff := time.Now().Add(-dispatchRateWindow)
	keep := b.timestamps[:0]
	for _, ts := range b.timestamps {
		if ts.After(cutoff) {
			keep = append(keep, ts)
		}
	}
	b.timestamps = keep
	if len(b.timestamps) >= dispatchRateMaxPerWindow {
		// 触发限流——下次允许时刻 = 最早的事件 + window
		oldest := b.timestamps[0]
		retry := time.Until(oldest.Add(dispatchRateWindow))
		if retry < time.Second {
			retry = time.Second
		}
		return false, retry
	}
	b.timestamps = append(b.timestamps, time.Now())
	return true, 0
}
