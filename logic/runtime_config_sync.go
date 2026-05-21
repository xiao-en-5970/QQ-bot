// runtime_config_sync.go —— 从 hfut 后端拉"bot 运行时配置"并热替换到内存。
//
// 数据流：
//
//	hfut bot_runtime_config 表 (admin UI 维护)
//	    → GET /api/v1/bot/runtime-config  （bot poll，默认 60s 一次）
//	    → 解码为 conf.RuntimeOverlay（按 key 分别解码：白名单 / 运维群 / silent_mode）
//	    → conf.SetRuntimeOverlay()  原子替换
//	    → 所有 helper（IsAutoReplyGroup / IsOpsGroup / IsSilent*）下一次读取自动用新值
//
// 兜底语义：
//   - hfut 未配置时 conf.Cfg.* 维持 env / yaml 加载的静态值（向后兼容旧部署）
//   - 启动时同步 fetch 一次（5s 超时）：失败 warn，不阻塞 bot 启动
//   - 之后每 60s poll 一次，失败保留上一次 snapshot
//   - global.Hfut 为 nil（HFUT_API_URL 未配） → 跳过整个同步流程
package logic

import (
	"context"
	"encoding/json"
	"qq_bot/conf"
	"qq_bot/global"
	zaplog "qq_bot/utils/zap"
	"time"
)

const (
	runtimeConfigPullInitDelay = 0
	runtimeConfigPullInterval  = 60 * time.Second
	runtimeConfigPullTimeout   = 5 * time.Second
)

// StartRuntimeConfigSync 启动远端配置同步循环（非阻塞）。
//
// 调用方在 main.go 已为本协程 wg.Add(1)；本函数任意路径都必须确保对应 wg.Done()
// 被调用，包括"hfut 未配置直接跳过"路径——否则 main 的 Wg.Wait() 永远等不到。
//
// 启动时尝试同步拉一次（5s 超时），让"启动后第一秒就生效"；失败保留 env 兜底。
func StartRuntimeConfigSync(ctx context.Context) {
	if global.Hfut == nil {
		// main 已 Wg.Add(1)；这里立刻 Done 抵消，否则 Wg.Wait 永远阻塞
		zaplog.Logger.Infof("runtime config sync: 未启用（HFUT_API_URL 为空）")
		global.Wg.Done()
		return
	}

	// 启动同步拉一次：减少 bot 启动到 hfut 配置生效之间的"环境变量兜底"窗口
	pullOnce(ctx)

	go func() {
		defer global.Wg.Done()
		if runtimeConfigPullInitDelay > 0 {
			select {
			case <-time.After(runtimeConfigPullInitDelay):
			case <-ctx.Done():
				return
			}
		}
		t := time.NewTicker(runtimeConfigPullInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				zaplog.Logger.Infof("runtime config sync: 协程退出")
				return
			case <-t.C:
				pullOnce(ctx)
			}
		}
	}()
}

// pullOnce 单次拉取 + 热替换。失败 warn 后 return，不更新 snapshot。
func pullOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, runtimeConfigPullTimeout)
	defer cancel()
	raw, err := global.Hfut.GetRuntimeConfig(ctx)
	if err != nil {
		zaplog.Logger.Warnf("runtime config sync: 拉取失败，沿用上一次配置: %v", err)
		return
	}
	overlay := parseOverlay(raw)
	conf.SetRuntimeOverlay(overlay)
	zaplog.Logger.Infof("runtime config sync: 已应用 keys=%d  whitelist=%v ops=%v silent=%v",
		len(raw), overlay.AutoReplyWhitelist, overlay.OpsGroupIDs, overlay.SilentMode)
}

// parseOverlay 把 hfut 返回的 key→json.RawMessage 解码为 RuntimeOverlay。
//
// 单个 key 解析失败仅 warn，不让坏数据导致整段 overlay 退回 env 兜底——这样
// admin 不小心写错了某一项时，至少其它 key 不会跟着失效。
func parseOverlay(raw map[string]json.RawMessage) *conf.RuntimeOverlay {
	out := &conf.RuntimeOverlay{}
	if v, ok := raw["auto_reply_whitelist"]; ok {
		var arr []int64
		if err := json.Unmarshal(v, &arr); err != nil {
			zaplog.Logger.Warnf("runtime config: auto_reply_whitelist 解析失败: %v", err)
		} else {
			out.AutoReplyWhitelist = arr
		}
	}
	if v, ok := raw["ops_group_ids"]; ok {
		var arr []int64
		if err := json.Unmarshal(v, &arr); err != nil {
			zaplog.Logger.Warnf("runtime config: ops_group_ids 解析失败: %v", err)
		} else {
			out.OpsGroupIDs = arr
		}
	}
	if v, ok := raw["silent_mode"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			zaplog.Logger.Warnf("runtime config: silent_mode 解析失败: %v", err)
		} else {
			out.SilentMode = &b
		}
	}
	return out
}
