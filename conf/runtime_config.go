// 运行时配置热替换：从 hfut 后端 /api/v1/bot/runtime-config 拉到的 KV 在内存里
// 用 atomic.Pointer 一次性 swap，所有读者无锁。env / yaml 里配的同名字段作为兜底
// （拉不到 / hfut 未配置 / bot 离线启动等情况）。
//
// 设计要点：
//   - 读快、零分配 → atomic.Pointer + 不可变 snapshot
//   - 写少（admin UI 改了配置之后 bot 下一次 poll，≤60s 一次） → poll 协程独立做
//   - helper 函数对外名字不变（IsAutoReplyGroup / IsOpsGroup / IsSilentForGroup / ...）
//   - **新部署没改 env 也兼容**：runtime snapshot 为 nil 时 fallback 到 env-loaded 字段

package conf

import (
	"sync/atomic"
)

// RuntimeOverlay 在 hfut DB 维护、被 bot 周期同步进来的"群相关运行时配置"。
//
// 三个字段一一对应 hfut bot_runtime_config 表里的 key（见 dao/bot_runtime_config.go）。
// nil 值代表 hfut 那一项**没维护**，由 env / yaml 兜底；不为 nil 代表"以 runtime 为准"。
type RuntimeOverlay struct {
	AutoReplyWhitelist []int64 // nil = 未配置，从 env 兜底
	OpsGroupIDs        []int64 // nil = 未配置，从 env 兜底
	SilentMode         *bool   // nil = 未配置，从 env 兜底
}

var runtimePtr atomic.Pointer[RuntimeOverlay]

// GetRuntimeOverlay 返回当前 snapshot；可能为 nil（首次拉取前 / 拉取失败且无历史）。
func GetRuntimeOverlay() *RuntimeOverlay {
	return runtimePtr.Load()
}

// SetRuntimeOverlay 原子替换 snapshot。调用方通常在拉到 hfut 返回后调用一次。
func SetRuntimeOverlay(o *RuntimeOverlay) {
	runtimePtr.Store(o)
}
