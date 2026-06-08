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
// 字段一一对应 hfut bot_runtime_config 表里的 key（见 dao/bot_runtime_config.go）。
// nil 值代表 hfut 那一项**没维护**，由 env / yaml 兜底；不为 nil 代表"以 runtime 为准"。
//
// 设计取舍——白名单 vs 静默：
//   - AutoReplyWhitelist：传统"可回复白名单"——bot 既识别又回复 ack
//   - SilentWhitelist：只读白名单——bot 同样监听 + 识别 + 落库，**但不在群里发任何反馈**
//     （运维群通知仍照常）。用于在真实校园群里跑识别准确率实测、不打扰群友。
//   - SilentMode：旧的全局开关——开启后任何非运维群都被静默。**逐步淘汰**，新部署
//     直接用 SilentWhitelist 更灵活（按群粒度，无需全局开关）。SilentMode 仍保留作
//     向后兼容（env / yaml 配 = true 时所有 AutoReplyWhitelist 群都按静默处理）。
//
// 设计取舍——黑名单关键词：
//   - 命中 BlockedKeywords 任一关键词的消息（含图片 OCR 结果）直接拦截，不送 Kimi
//     识别、不落库、不回复。用于过滤明显的违规品类（家教/兼职/代考/出国/车队等
//     校园生态不希望承载的内容）。
type RuntimeOverlay struct {
	AutoReplyWhitelist []int64  // 可回复白名单：监听 + 识别 + 回复
	SilentWhitelist    []int64  // 只读白名单：监听 + 识别 + 落库；**不在群里发任何反馈**
	OpsGroupIDs        []int64  // 运维群
	SilentMode         *bool    // 旧全局静默开关（向后兼容；建议用 SilentWhitelist 替代）
	BlockedKeywords    []string // 关键词黑名单：含任一关键词的消息直接拦截
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
