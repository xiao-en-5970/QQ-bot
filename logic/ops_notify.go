// Package logic 的 ops_notify.go 把"对内通知"统一发到 conf.Bot.OpsGroupID。
//
// 触发场景：
//   - 任何成功的上架（publish_good / publish_question / seek_goods）
//   - 群管理员私聊申请接入 bot
//   - 其它需要运营盯的关键事件（限流命中、识别失败长时间、quota 冷却等可后续接入）
//
// 当 conf.Bot.OpsGroupID == 0 时本文件所有函数都静默 no-op。任何发送失败仅 log warn，
// 不让"通知"挡到主路径业务（上架成功 ≠ 通知发出，业务层不依赖通知是否到达）。
package logic

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"qq_bot/conf"
	"qq_bot/utils/metrics"
	zaplog "qq_bot/utils/zap"
)

// NotifyOps 把一段文本发到运维群。
//
// httpClient 来自调用方（保持与触发动作同一连接池友好），nil 时退化用全局默认 client——
// 多数 dispatch 路径都已经带了 client，nil 仅作为防御兜底。
//
// 返回 error 仅供调用方记录；所有 dispatch 路径都不应该因为 NotifyOps 失败而失败。
func NotifyOps(client *http.Client, text string) {
	gid := conf.Cfg.Bot.OpsGroupID
	if gid == 0 {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if client == nil {
		client = http.DefaultClient
	}
	if err := SendGroupText(client, gid, text); err != nil {
		zaplog.Logger.Warnf("ops notify 发送失败 ops_group=%d: %v err_text=%q", gid, err, truncateForLog(text, 200))
		return
	}
	metrics.IncOpsNotify()
}

// NotifyOpsPublish 标准化"上架事件"文案——所有 publish_* / seek_goods 成功后调一下。
//
// 字段：
//
//	groupID    用户在哪个群发起的上架（业务来源群）
//	userID     发起人 QQ 号
//	userCard   群名片 / nickname（若未知传空）
//	kind       人类可读的种类：二手 / 求物品 / 求解答 / 求物品-求购等
//	title      标题（已清理空白）
//	extra      可选附加摘要：价格 / 地点 / 配图数等，多行用 ' / ' 分隔
func NotifyOpsPublish(client *http.Client, groupID, userID int64, userCard, kind, title string, extra ...string) {
	if conf.Cfg.Bot.OpsGroupID == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] 上架\n来源群: %d\n用户: %d", time.Now().Format("01-02 15:04:05"), groupID, userID)
	if c := strings.TrimSpace(userCard); c != "" {
		fmt.Fprintf(&b, " (%s)", c)
	}
	fmt.Fprintf(&b, "\n类别: %s\n标题: %s", kind, orPlaceholder(title, "(无)"))
	for _, e := range extra {
		e = strings.TrimSpace(e)
		if e != "" {
			b.WriteByte('\n')
			b.WriteString(e)
		}
	}
	NotifyOps(client, b.String())
}

// NotifyOpsGroupAccessRequest 群管理员私聊申请把 bot 能力接入 X 群——把请求摘要发运维群。
//
//	requesterQQ   发起人 QQ 号
//	requesterName 发起人昵称（getStrangerInfo / sender.nickname）
//	targetGroup   目标群号
//	role          申请人在目标群的角色（owner / admin / member / unknown / not_in_group）
//	rawText       发起人原始消息（截断后），方便人工核查
func NotifyOpsGroupAccessRequest(client *http.Client, requesterQQ int64, requesterName string, targetGroup int64, role string, rawText string) {
	if conf.Cfg.Bot.OpsGroupID == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] 群接入申请", time.Now().Format("01-02 15:04:05"))
	fmt.Fprintf(&b, "\n申请人: %d", requesterQQ)
	if n := strings.TrimSpace(requesterName); n != "" {
		fmt.Fprintf(&b, " (%s)", n)
	}
	fmt.Fprintf(&b, "\n目标群: %d", targetGroup)
	fmt.Fprintf(&b, "\n身份: %s", role)
	if t := strings.TrimSpace(rawText); t != "" {
		fmt.Fprintf(&b, "\n原文: %s", truncateForLog(t, 200))
	}
	NotifyOps(client, b.String())
}
