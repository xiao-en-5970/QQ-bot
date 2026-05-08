// Package metrics 维护 QQ-bot 进程级运行计数 + 最近事件 + 每分钟时序，
// 供 internal API /internal/metrics 端点输出，再由 hfut admin 面板展示。
//
// 设计：
//
//   - 计数器全部 atomic，不引入锁；快照时一次性 Load。
//   - 最近事件 ring buffer（默认 50）：保留近期上架 / 求物品 / 下架事件的细节，
//     方便运维面板 hover 看到 "时间 + 群 + 用户 + 标题 + 模型 reason + outcome"。
//   - 每分钟时序：保留过去 60 分钟，按 (epochMinute -> bucket) 聚合，给前端画折线。
//   - 不引入外部 TSDB；进程重启清零。
package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// recentEventsCap 保留最近 N 条 dispatch 事件
const recentEventsCap = 50

// seriesWindowMin 时序保留的窗口大小（分钟）
const seriesWindowMin = 60

// DispatchEvent 单条 dispatch 事件——前端表格展示用。
type DispatchEvent struct {
	TS         string  `json:"ts"`
	GroupID    int64   `json:"group_id"`
	UserID     int64   `json:"user_id"`
	UserCard   string  `json:"user_card,omitempty"`
	ActionType string  `json:"action_type"`
	Outcome    string  `json:"outcome"`
	Title      string  `json:"title,omitempty"`
	Category   int     `json:"category,omitempty"`
	Negotiable bool    `json:"negotiable,omitempty"`
	Price      float64 `json:"price,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	AckText    string  `json:"ack_text,omitempty"`
}

// RecordDispatchInput 调用 RecordDispatchEvent 的参数。把字段拆开避免上层 import 循环。
type RecordDispatchInput struct {
	GroupID    int64
	UserID     int64
	UserCard   string
	ActionType string
	Outcome    string
	Title      string
	Category   int
	Negotiable bool
	Price      float64
	Confidence float64
	Reason     string
	AckText    string
}

type counters struct {
	startedAt time.Time

	// WebSocket 入站
	wsGroupMsgs   atomic.Int64
	wsPrivateMsgs atomic.Int64
	wsIgnored     atomic.Int64

	// 自动识别（Kimi）
	recognizeCalled  atomic.Int64
	recognizeSuccess atomic.Int64
	recognizeFail    atomic.Int64
	quotaCooling     atomic.Int64

	// dispatch 结果（按 action 维度），由调用方传 actionType / outcome
	mu       sync.Mutex
	dispatch map[string]*atomic.Int64

	// 私聊业务
	privateAccessReq atomic.Int64

	// 限流命中
	rateLimitHits atomic.Int64

	// 运维通知
	opsNotifications atomic.Int64

	// 最近事件 ring buffer（dispatch 全量明细）
	eventsMu sync.Mutex
	events   []DispatchEvent

	// 每分钟时序：epochMinute -> {ws, recognize, dispatch_success}
	seriesMu sync.Mutex
	series   map[int64]*minuteBucket
}

// minuteBucket 一分钟内的累计；切到下一分钟会新建。
//
// 字段尽量"扁平 + 直白"，因为 hfut 端 metrics_persister 会按 json tag 名把每个字段
// 落到 metric_minute(source='bot', metric=<tag>)；面板再按时间窗 SUM 拼回累计值。
//
// 新增字段必须**只增不删不改名**——历史 metric_minute 行用的就是这些 tag 作为 metric 名，
// 改名会让历史数据查不出来。
type minuteBucket struct {
	WSMsgs           int64 `json:"ws_msgs"`         // 群+私聊合计
	WSGroupMsgs      int64 `json:"ws_group_msgs"`   // 仅群消息
	WSPrivateMsgs    int64 `json:"ws_private_msgs"` // 仅私聊
	RecognizeCalled  int64 `json:"recognize_called"`
	RecognizeSuccess int64 `json:"recognize_success"`
	RecognizeFail    int64 `json:"recognize_fail"`
	QuotaCooling     int64 `json:"quota_cooling"`
	DispatchSuccess  int64 `json:"dispatch_success"`
	DispatchFail     int64 `json:"dispatch_fail"`
	DispatchOther    int64 `json:"dispatch_other"` // dup / ignore / ask_user 等
	RateLimit        int64 `json:"rate_limit"`
	PrivateAccess    int64 `json:"private_access"`
	OpsNotify        int64 `json:"ops_notify"`
}

var c = &counters{
	startedAt: time.Now(),
	dispatch:  make(map[string]*atomic.Int64),
	events:    make([]DispatchEvent, 0, recentEventsCap),
	series:    make(map[int64]*minuteBucket),
}

func currentMinute() int64 { return time.Now().Truncate(time.Minute).Unix() }

// touchSeries 在当前分钟的 bucket 上调 fn(b)，桶不存在自动创建并 GC 老桶。
func touchSeries(fn func(*minuteBucket)) {
	c.seriesMu.Lock()
	defer c.seriesMu.Unlock()
	now := currentMinute()
	b, ok := c.series[now]
	if !ok {
		b = &minuteBucket{}
		c.series[now] = b
		// GC 老桶——保留最近 seriesWindowMin 分钟
		cutoff := now - int64(seriesWindowMin*60)
		for k := range c.series {
			if k < cutoff {
				delete(c.series, k)
			}
		}
	}
	fn(b)
}

// IncWSMessage 收到一条来自 NapCat 的消息事件。
func IncWSMessage(kind string) {
	switch kind {
	case "group":
		c.wsGroupMsgs.Add(1)
	case "private":
		c.wsPrivateMsgs.Add(1)
	default:
		c.wsIgnored.Add(1)
	}
	if kind == "group" || kind == "private" {
		touchSeries(func(b *minuteBucket) {
			b.WSMsgs++
			if kind == "group" {
				b.WSGroupMsgs++
			} else {
				b.WSPrivateMsgs++
			}
		})
	}
}

// IncRecognize 调一次 Kimi 识别，outcome ∈ {success, fail, quota_cooling}。
func IncRecognize(outcome string) {
	c.recognizeCalled.Add(1)
	switch outcome {
	case "success":
		c.recognizeSuccess.Add(1)
	case "quota_cooling":
		c.quotaCooling.Add(1)
	default:
		c.recognizeFail.Add(1)
	}
	touchSeries(func(b *minuteBucket) {
		b.RecognizeCalled++
		switch outcome {
		case "success":
			b.RecognizeSuccess++
		case "quota_cooling":
			b.QuotaCooling++
		default:
			b.RecognizeFail++
		}
	})
}

// IncDispatch 一条 action 的分发结果，按 actionType + outcome 分桶。
//
// outcome 推荐取值：success / dup / fail / ignore / ask_user。
// 时序桶里 success / fail 单独记，其余统一进 dispatch_other 避免 cardinality 爆炸；
// cumulative dispatch map 仍按 action:outcome 全维度细分（snapshot 时整体输出）。
func IncDispatch(actionType, outcome string) {
	key := actionType + ":" + outcome
	c.mu.Lock()
	bucket, ok := c.dispatch[key]
	if !ok {
		bucket = &atomic.Int64{}
		c.dispatch[key] = bucket
	}
	c.mu.Unlock()
	bucket.Add(1)
	touchSeries(func(b *minuteBucket) {
		switch outcome {
		case "success":
			b.DispatchSuccess++
		case "fail":
			b.DispatchFail++
		default:
			b.DispatchOther++
		}
	})
}

// RecordDispatchEvent 把一次识别 + dispatch 的全量明细放入最近事件 ring。
//
// 调用时机：autoReply 收到 ackResult 后。即便 outcome=ignore 也记一条，便于排查"为什么没有响应"。
func RecordDispatchEvent(in RecordDispatchInput) {
	ev := DispatchEvent{
		TS:         time.Now().Format(time.RFC3339),
		GroupID:    in.GroupID,
		UserID:     in.UserID,
		UserCard:   in.UserCard,
		ActionType: in.ActionType,
		Outcome:    in.Outcome,
		Title:      in.Title,
		Category:   in.Category,
		Negotiable: in.Negotiable,
		Price:      in.Price,
		Confidence: in.Confidence,
		Reason:     in.Reason,
		AckText:    in.AckText,
	}
	c.eventsMu.Lock()
	defer c.eventsMu.Unlock()
	c.events = append(c.events, ev)
	if len(c.events) > recentEventsCap {
		c.events = c.events[len(c.events)-recentEventsCap:]
	}
}

// IncPrivateAccessRequest 群接入申请数。
func IncPrivateAccessRequest() {
	c.privateAccessReq.Add(1)
	touchSeries(func(b *minuteBucket) { b.PrivateAccess++ })
}

// IncRateLimit dispatch 限流命中。
func IncRateLimit() {
	c.rateLimitHits.Add(1)
	touchSeries(func(b *minuteBucket) { b.RateLimit++ })
}

// IncOpsNotify 给运维群发了一条通知。
func IncOpsNotify() {
	c.opsNotifications.Add(1)
	touchSeries(func(b *minuteBucket) { b.OpsNotify++ })
}

// Snapshot 当前计数快照——/internal/metrics 端点输出用。
func Snapshot() map[string]any {
	c.mu.Lock()
	dispatch := make(map[string]int64, len(c.dispatch))
	for k, v := range c.dispatch {
		dispatch[k] = v.Load()
	}
	c.mu.Unlock()

	c.eventsMu.Lock()
	events := make([]DispatchEvent, len(c.events))
	copy(events, c.events)
	c.eventsMu.Unlock()

	// 时序按时间升序输出，最多 seriesWindowMin 个点。
	// 字段保持与 minuteBucket 一致——hfut persister 直接按 json key 落盘。
	type seriesPoint struct {
		Minute           int64 `json:"minute"`
		WSMsgs           int64 `json:"ws_msgs"`
		WSGroupMsgs      int64 `json:"ws_group_msgs"`
		WSPrivateMsgs    int64 `json:"ws_private_msgs"`
		RecognizeCalled  int64 `json:"recognize_called"`
		RecognizeSuccess int64 `json:"recognize_success"`
		RecognizeFail    int64 `json:"recognize_fail"`
		QuotaCooling     int64 `json:"quota_cooling"`
		DispatchSuccess  int64 `json:"dispatch_success"`
		DispatchFail     int64 `json:"dispatch_fail"`
		DispatchOther    int64 `json:"dispatch_other"`
		RateLimit        int64 `json:"rate_limit"`
		PrivateAccess    int64 `json:"private_access"`
		OpsNotify        int64 `json:"ops_notify"`
	}
	c.seriesMu.Lock()
	keys := make([]int64, 0, len(c.series))
	for k := range c.series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	points := make([]seriesPoint, 0, len(keys))
	for _, k := range keys {
		b := c.series[k]
		points = append(points, seriesPoint{
			Minute:           k,
			WSMsgs:           b.WSMsgs,
			WSGroupMsgs:      b.WSGroupMsgs,
			WSPrivateMsgs:    b.WSPrivateMsgs,
			RecognizeCalled:  b.RecognizeCalled,
			RecognizeSuccess: b.RecognizeSuccess,
			RecognizeFail:    b.RecognizeFail,
			QuotaCooling:     b.QuotaCooling,
			DispatchSuccess:  b.DispatchSuccess,
			DispatchFail:     b.DispatchFail,
			DispatchOther:    b.DispatchOther,
			RateLimit:        b.RateLimit,
			PrivateAccess:    b.PrivateAccess,
			OpsNotify:        b.OpsNotify,
		})
	}
	c.seriesMu.Unlock()

	// 把最近事件按时间倒序展示（最新在前）
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return map[string]any{
		"started_at":              c.startedAt.Format(time.RFC3339),
		"uptime_seconds":          int64(time.Since(c.startedAt).Seconds()),
		"ws_group_msgs":           c.wsGroupMsgs.Load(),
		"ws_private_msgs":         c.wsPrivateMsgs.Load(),
		"ws_ignored":              c.wsIgnored.Load(),
		"recognize_called":        c.recognizeCalled.Load(),
		"recognize_success":       c.recognizeSuccess.Load(),
		"recognize_fail":          c.recognizeFail.Load(),
		"quota_cooling":           c.quotaCooling.Load(),
		"dispatch":                dispatch,
		"private_access_requests": c.privateAccessReq.Load(),
		"rate_limit_hits":         c.rateLimitHits.Load(),
		"ops_notifications":       c.opsNotifications.Load(),
		"recent_events":           events,
		"series":                  points,
	}
}
