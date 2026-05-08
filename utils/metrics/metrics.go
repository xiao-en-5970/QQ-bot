// Package metrics 维护 QQ-bot 进程级运行计数，给 internal API /internal/metrics 端点输出。
//
// 设计：
//
//   - 全部 atomic 计数，不引入锁；Snapshot 时一次性 Load 所有字段。
//   - 不依赖外部 TSDB / Prometheus；进程重启清零；够给 admin 面板查询用。
//   - 业务上有意义的桶按"事件类型"分：消息接收 / Kimi 识别 / dispatch 结果 /
//     私聊请求 / 群接入申请 / 限流命中 / quotaGate 状态。
//
// 调用方式：
//
//	metrics.IncWSMessage("group")
//	metrics.IncDispatch("publish_good", "success")
package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

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
}

var c = &counters{
	startedAt: time.Now(),
	dispatch:  make(map[string]*atomic.Int64),
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
}

// IncDispatch 一条 action 的分发结果，按 actionType + outcome 分桶。
//
// outcome 推荐取值：success / dup / fail / ignore / ask_user。
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
}

// IncPrivateAccessRequest 群接入申请数。
func IncPrivateAccessRequest() { c.privateAccessReq.Add(1) }

// IncRateLimit dispatch 限流命中。
func IncRateLimit() { c.rateLimitHits.Add(1) }

// IncOpsNotify 给运维群发了一条通知。
func IncOpsNotify() { c.opsNotifications.Add(1) }

// Snapshot 当前计数快照——/internal/metrics 端点输出用。
func Snapshot() map[string]any {
	c.mu.Lock()
	dispatch := make(map[string]int64, len(c.dispatch))
	for k, v := range c.dispatch {
		dispatch[k] = v.Load()
	}
	c.mu.Unlock()

	return map[string]any{
		"started_at":      c.startedAt.Format(time.RFC3339),
		"uptime_seconds":  int64(time.Since(c.startedAt).Seconds()),
		"ws_group_msgs":   c.wsGroupMsgs.Load(),
		"ws_private_msgs": c.wsPrivateMsgs.Load(),
		"ws_ignored":      c.wsIgnored.Load(),
		"recognize_called":  c.recognizeCalled.Load(),
		"recognize_success": c.recognizeSuccess.Load(),
		"recognize_fail":    c.recognizeFail.Load(),
		"quota_cooling":     c.quotaCooling.Load(),
		"dispatch":          dispatch,
		"private_access_requests": c.privateAccessReq.Load(),
		"rate_limit_hits":         c.rateLimitHits.Load(),
		"ops_notifications":       c.opsNotifications.Load(),
	}
}
