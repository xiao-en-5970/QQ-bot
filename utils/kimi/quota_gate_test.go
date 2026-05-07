package kimi

import (
	"errors"
	"testing"
	"time"

	"qq_bot/conf"
)

// resetQuotaGate 单测里复位全局 gate；测试间状态污染会让 t.Run 互相干扰。
func resetQuotaGate(t *testing.T) {
	t.Helper()
	globalQuotaGate.mu.Lock()
	globalQuotaGate.consecutiveErrors = 0
	globalQuotaGate.cooldownUntil = time.Time{}
	globalQuotaGate.mu.Unlock()
}

// setQuotaGateConf 设置 conf 里 gate 用的 threshold + cooldown 秒（最小粒度 1s）。
// 时长字段是秒级整数（贴合 conf 真实接口），跟生产保持一致；自动恢复测试因此用 1s。
func setQuotaGateConf(threshold int, cooldownSeconds int) {
	conf.Cfg.Gpt.QuotaErrorThreshold = threshold
	conf.Cfg.Gpt.QuotaCooldownSeconds = cooldownSeconds
}

func TestQuotaGate_OpenWhenNoErrors(t *testing.T) {
	resetQuotaGate(t)
	setQuotaGateConf(3, 1800)
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		t.Fatalf("初始 gate 应当是 open 的，结果 blocked=true")
	}
}

func TestQuotaGate_NonQuotaErrorDoesNotTrip(t *testing.T) {
	resetQuotaGate(t)
	setQuotaGateConf(3, 1800)
	for i := 0; i < 10; i++ {
		// 一连串"网络错误" / "解析错误" 不应该触发 gate
		globalQuotaGate.RecordResult(errors.New("connection reset by peer"))
	}
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		t.Fatalf("非 quota 错误不应触发熔断；当前 blocked=true")
	}
}

func TestQuotaGate_TripAfterThreshold(t *testing.T) {
	resetQuotaGate(t)
	setQuotaGateConf(3, 1800)
	quotaErr := errors.New("[exceeded_current_quota_error]Your project ... exceeded budget")

	globalQuotaGate.RecordResult(quotaErr)
	globalQuotaGate.RecordResult(quotaErr)
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		t.Fatalf("threshold=3 时第 2 次 quota 错不应该触发；当前 blocked=true")
	}
	globalQuotaGate.RecordResult(quotaErr) // 第 3 次 → trip
	blocked, remain := globalQuotaGate.IsBlocked()
	if !blocked {
		t.Fatalf("第 3 次 quota 错应当触发熔断；当前 blocked=false")
	}
	if remain <= 0 || remain > 30*time.Minute {
		t.Fatalf("冷却剩余时间应在 (0, 30min] 区间；得到 %v", remain)
	}
}

func TestQuotaGate_NonQuotaErrorResetsCounter(t *testing.T) {
	resetQuotaGate(t)
	setQuotaGateConf(3, 1800)
	quotaErr := errors.New("[exceeded_current_quota_error] no money left")
	globalQuotaGate.RecordResult(quotaErr)
	globalQuotaGate.RecordResult(quotaErr)
	// 中间穿插一个非 quota 错 → 计数清零
	globalQuotaGate.RecordResult(errors.New("network timeout"))
	globalQuotaGate.RecordResult(quotaErr)
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		t.Fatalf("非 quota 错应当 reset 计数；现在不该熔断")
	}
}

func TestQuotaGate_AutoRecoverAfterCooldown(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in -short：依赖 1s 冷却时长")
	}
	resetQuotaGate(t)
	setQuotaGateConf(2, 1) // 1 秒冷却（QuotaCooldownSeconds 是秒级 int）
	quotaErr := errors.New("[exceeded_current_quota_error]")
	globalQuotaGate.RecordResult(quotaErr)
	globalQuotaGate.RecordResult(quotaErr) // trip
	if blocked, _ := globalQuotaGate.IsBlocked(); !blocked {
		t.Fatalf("应当处于冷却期")
	}
	time.Sleep(1100 * time.Millisecond)
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		t.Fatalf("冷却到点应当自动恢复 open；当前仍 blocked=true")
	}
}

func TestQuotaGate_SuccessResetsCounter(t *testing.T) {
	resetQuotaGate(t)
	setQuotaGateConf(3, 1800)
	quotaErr := errors.New("[exceeded_current_quota_error]")
	globalQuotaGate.RecordResult(quotaErr)
	globalQuotaGate.RecordResult(quotaErr)
	globalQuotaGate.RecordResult(nil) // 成功一次
	globalQuotaGate.RecordResult(quotaErr)
	globalQuotaGate.RecordResult(quotaErr)
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		t.Fatalf("成功调用应当 reset 计数；现在第 5 次 quota 错才相当于 reset 后第 2 次，不该熔断")
	}
}

func TestIsQuotaError(t *testing.T) {
	cases := []struct {
		err   error
		quota bool
	}{
		{nil, false},
		{errors.New("connection refused"), false},
		{errors.New("[exceeded_current_quota_error]Your project xxx exceeded budget"), true},
		{errors.New("[rate_limit_reached_error]too many requests"), true},
		{errors.New("[invalid_authentication_error]wrong key"), false},
	}
	for _, c := range cases {
		got := isQuotaError(c.err)
		if got != c.quota {
			t.Errorf("isQuotaError(%v) = %v; want %v", c.err, got, c.quota)
		}
	}
}
