package kimi

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"qq_bot/conf"
	zaplog "qq_bot/utils/zap"
)

// quotaGate 是包级 LLM 调用熔断器。
//
// 动机：日志里观察到生产 ak 配额耗尽时，每条群消息都会原样撞 Moonshot 一次拿到
// `exceeded_current_quota_error`。41 小时刷出 60+ 条同样的 ERROR 日志、浪费 RTT、
// 且扰乱真问题的可见度。引入 gate 后行为：
//
//  1. 任何调用 Kimi 的入口（Chat / RecognizeBusinessActions）调用前先 IsBlocked()
//  2. 拿到 err 后调 RecordResult(err) —— quota 错累计，其他错或成功都重置计数
//  3. 连续 ≥ threshold 次 quota 错 → 进入冷却 cooldown 时长，期间 IsBlocked 返回 true
//  4. cooldown 到点自动恢复（lazy：下一次 IsBlocked 看时间戳）
//
// 不引入额外 goroutine——所有状态切换在调用线程内完成，避免锁/定时器复杂度。
type quotaGate struct {
	mu                sync.Mutex
	consecutiveErrors int
	cooldownUntil     time.Time
}

var globalQuotaGate quotaGate

// IsBlocked 返回当前是否处于冷却期；处于则 LLM 入口应当 short-circuit 不要再调 API。
//
// 第二个返回值是冷却剩余时间，方便上层 log / ack 提示用户。
func (q *quotaGate) IsBlocked() (bool, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cooldownUntil.IsZero() {
		return false, 0
	}
	now := time.Now()
	if now.After(q.cooldownUntil) {
		// 冷却到点，自动重置；下一次实际调用如果还是 quota 错会再次进入冷却
		q.cooldownUntil = time.Time{}
		q.consecutiveErrors = 0
		return false, 0
	}
	return true, q.cooldownUntil.Sub(now)
}

// RecordResult 把这一次 LLM 调用结果（err 即可，nil = 成功）反馈给 gate。
//
// quota 错累计计数；非 quota 错或成功都把连续计数清零（避免偶发网络错误把
// gate 推进冷却）。命中 threshold 后启动冷却。
func (q *quotaGate) RecordResult(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if err == nil {
		q.consecutiveErrors = 0
		return
	}
	if !IsQuotaError(err) {
		// 非 quota 错（网络抖、moonshot 5xx、JSON parse 失败等）不算进来——避免把
		// 偶发问题升级成全局熔断
		q.consecutiveErrors = 0
		return
	}

	q.consecutiveErrors++
	threshold := conf.Cfg.Gpt.QuotaErrorThreshold
	if threshold <= 0 {
		threshold = 3
	}
	if q.consecutiveErrors < threshold {
		return
	}
	cooldownSeconds := conf.Cfg.Gpt.QuotaCooldownSeconds
	if cooldownSeconds <= 0 {
		cooldownSeconds = 1800
	}
	q.cooldownUntil = time.Now().Add(time.Duration(cooldownSeconds) * time.Second)
	// 防御性 nil-check：单测可能没初始化全局 logger
	if zaplog.Logger != nil {
		zaplog.Logger.Warnf("quotaGate 触发熔断：连续 %d 次 quota error，冷却到 %s（%ds）；期间 LLM 调用 short-circuit",
			q.consecutiveErrors, q.cooldownUntil.Format("15:04:05"), cooldownSeconds)
	}
}

// IsQuotaError 判断 err 是否是 Moonshot 配额耗尽错误。
//
// Moonshot 错误格式：`[exceeded_current_quota_error]Your project ...`，所以走字符串匹配。
// 如果哪天 SDK 暴露强类型错误（typed error）我们改成 errors.Is/As 即可。
//
// 暴露给 logic 层使用——quota gate 触发熔断需要 ≥ threshold 次累积，但单条消息不该
// 被白白 drop；上层检测到 raw quota error 时就该立即走 regex 兜底。
func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// 命中两类信号都算（保险起见）：
	//   - exceeded_current_quota_error  ← 我们日志里见到的真错码
	//   - rate_limit                    ← Moonshot 把 rate limit 也归这一类时兜底
	return strings.Contains(s, "exceeded_current_quota_error") ||
		strings.Contains(s, "rate_limit_reached_error")
}

// gateStatusForLog 给 log / 单测用——一行返回当前 gate 内部状态，方便排查。
func (q *quotaGate) gateStatusForLog() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cooldownUntil.IsZero() {
		return "open(consecutive=" + strconv.Itoa(q.consecutiveErrors) + ")"
	}
	return "cooling(consecutive=" + strconv.Itoa(q.consecutiveErrors) +
		", until=" + q.cooldownUntil.Format("15:04:05") + ")"
}
