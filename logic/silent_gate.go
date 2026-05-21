// 灰度静默 (SilentMode) 的统一发送门——所有 bot 对外消息出口在执行 NapCat
// 调用前都先过这里，命中静默时 log + 记 metric，再直接返回 nil（让上游业务继续
// 跑，只是用户感受不到 bot）。
//
// 静默语义（详见 conf.Bot.SilentMode 注释）：
//   - SilentMode == false → 永远不静默
//   - SilentMode == true  →
//       - 群消息：目标群不在 OpsGroupIDs 中即静默
//       - 群文件上传：目标群不在 OpsGroupIDs 中即静默
//       - 私聊：**不静默**（QQ 绑定 / 解绑验证码、订单加急等真实用户触发的链路保留）
//
// 为什么 gate 在 logic 层而不在 service 层：
//   - service 层 (send_group_msg.go 等) 是 NapCat HTTP 原始封装，给运维 / 命令 / hfut
//     调用方都用同一套；如果 gate 加在 service 层，会连"运维群正常通知"也被拦下。
//   - logic 层每个 send_* 函数都有 groupID / userID 参数，正好做"目标群是否运维群"的
//     判定，并能在 log 里打出具体被静默的目标和内容前缀，便于排查。

package logic

import (
	"qq_bot/conf"
	"qq_bot/utils/metrics"
	zaplog "qq_bot/utils/zap"
)

// silentSuppressGroup 静默模式下决定群消息是否要被屏蔽。
//
// 命中即返回 true，并打 debug 日志 + 自增 metric。调用方应在此情况下不要再走
// NapCat 发送，直接 return nil（让业务流程认为发送"成功"，配合上游 ack 不依赖
// 真实回执文本的设计）。
//
// 第二个参数 textForLog 是"被屏蔽的文本前缀"，只为人眼排查；nil 安全。
func silentSuppressGroup(groupID int64, textForLog string) bool {
	if !conf.Cfg.Bot.IsSilentForGroup(groupID) {
		return false
	}
	zaplog.Logger.Debugf("[silent] 屏蔽群消息 group=%d text=%q",
		groupID, truncateForSilentLog(textForLog, 80))
	metrics.IncSilentSuppressed()
	return true
}

// silentSuppressPrivate 私聊永不静默——SilentMode 只针对群消息出口。
//
// 为什么单独留这个函数而不直接删 SendPrivateText 里的调用：
//   - 保留 hook 点便于未来再细分（比如真要禁某类私聊时可以加白名单）
//   - 函数签名和 silentSuppressGroup 对称，调用方逻辑更整齐
//
// 详见 conf.Bot.IsSilentForPrivate 注释——返回 false 即让所有私聊出口照常下发。
func silentSuppressPrivate(qq int64, textForLog string) bool {
	if !conf.Cfg.Bot.IsSilentForPrivate() {
		return false
	}
	zaplog.Logger.Debugf("[silent] 屏蔽私聊 qq=%d text=%q",
		qq, truncateForSilentLog(textForLog, 80))
	metrics.IncSilentSuppressed()
	return true
}

// silentSuppressGroupFile 群文件上传的静默判定。
//
// 跟 silentSuppressGroup 同语义，单独拆出来只是为了让 log 信息明确写"群文件"。
func silentSuppressGroupFile(groupID int64, fileName string) bool {
	if !conf.Cfg.Bot.IsSilentForGroup(groupID) {
		return false
	}
	zaplog.Logger.Debugf("[silent] 屏蔽群文件上传 group=%d file=%q", groupID, fileName)
	metrics.IncSilentSuppressed()
	return true
}

func truncateForSilentLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
