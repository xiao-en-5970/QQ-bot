// Package logic 的 handle_private_message.go 处理 NapCat 推过来的"私聊"消息事件。
//
// 当前唯一实现的私聊业务：**群管理员 / 群主 申请把 bot 接入指定 QQ 群**。
//
// 流程：
//
//	1) 用户私聊 bot：含意图词 + 群号；如「我是群 123456 的管理员，把 bot 接入这个群」
//	2) bot 用 NapCat get_group_member_info(target_group, user_id) 校验申请人在该群的角色
//	3) 把申请摘要 forward 到 conf.Bot.OpsGroupID 运维群（含目标群号 / 申请人 / 角色 / 原文）
//	4) 私聊回执给申请人：已上报运营审核
//
// 不审核 bot 自身的"已加入 target group"——若 bot 没在该群，get_group_member_info 会失败，
// 上层兜底为 role="not_in_group"，仍然上报让运营人工跟进。
//
// 限流：同一 (caller, target_group) 5 分钟内只允许申请 1 次，避免被骚扰刷屏。
package logic

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"qq_bot/conf"
	"qq_bot/model"
	"qq_bot/service"
	"qq_bot/utils/metrics"
	zaplog "qq_bot/utils/zap"
)

// reAccessRequestGroupID 从私聊文本里抠出「群 <群号>」要接入的群号。
//
// 命中策略：先尝试明确的"接入 / 接进 / 加入 / 申请 / 接 bot"等动词关键字；
// 命中任意一个动词后，从原文里抓 5~12 位连续数字作为群号。
//
// 同时容忍：
//   - 「群 123456 接入 bot」「我是群 123456 的群主」「把 bot 加进群 123456」「123456 这个群想接入」
//   - 群号前后可有零至若干空格 / 「群」 / 「QQ群」 / 「Q群」 等装饰词
//
// 不接受："你帮我看看 bot 怎么用"——没群号 + 没动词。
var (
	reAccessVerb    = regexp.MustCompile(`接入|接进|加入|入驻|开通|申请\s*接|开启|启用\s*bot|接\s*bot|拉\s*bot|让\s*bot|想用\s*bot|想要\s*bot|加\s*bot`)
	reAccessGroupID = regexp.MustCompile(`(?:群|QQ群|Q群)?\s*([1-9]\d{4,11})`)
)

// accessRequestRateWindow 同一 (caller, target_group) 5 分钟限流窗口
const accessRequestRateWindow = 5 * time.Minute

type accessRequestKey struct {
	CallerQQ    int64
	TargetGroup int64
}

var (
	accessRequestMu      sync.Mutex
	accessRequestLastAt  = map[accessRequestKey]time.Time{}
	accessRequestCleanAt time.Time
)

// allowAccessRequest 限流 + 顺手清理 5 分钟前的过期项。
func allowAccessRequest(k accessRequestKey, now time.Time) bool {
	accessRequestMu.Lock()
	defer accessRequestMu.Unlock()
	// 每 10 分钟做一次惰性清理，避免 map 无限增长
	if now.Sub(accessRequestCleanAt) > 10*time.Minute {
		for kk, t := range accessRequestLastAt {
			if now.Sub(t) > accessRequestRateWindow {
				delete(accessRequestLastAt, kk)
			}
		}
		accessRequestCleanAt = now
	}
	if t, ok := accessRequestLastAt[k]; ok && now.Sub(t) < accessRequestRateWindow {
		return false
	}
	accessRequestLastAt[k] = now
	return true
}

// HandlePrivateMessage wsclient 收到 message_type=private 事件后调用本函数。
//
// 不会阻塞 wsclient 协程——所有耗时操作（NapCat API、网络）都在本函数内同步做，
// 但调用方 `go HandlePrivateMessage(...)` 启动协程后立刻返回。
func HandlePrivateMessage(client *http.Client, msg *model.Message) {
	if msg == nil || msg.UserID == 0 {
		return
	}
	// 防回响：bot 自己发出去的私聊也会以 message_sent 推回；这里再兜一层。
	if conf.Cfg.User.UserID != nil && msg.UserID == *conf.Cfg.User.UserID {
		return
	}

	flat := flattenMessageText(msg)
	if strings.TrimSpace(flat) == "" {
		return
	}
	zaplog.Logger.Infof("HandlePrivateMessage from=%d text=%q", msg.UserID, truncateForLog(flat, 200))

	if targetGroup, ok := parseAccessRequest(flat); ok {
		metrics.IncPrivateAccessRequest()
		handleGroupAccessRequest(client, msg, flat, targetGroup)
		return
	}

	// 其它私聊文本：暂不响应，避免对管理员之外的用户骚扰式回复
	zaplog.Logger.Debugf("HandlePrivateMessage 未匹配任何意图，静默忽略 from=%d", msg.UserID)
}

// parseAccessRequest 从私聊文本里抠 (动词 + 群号)；命中返回目标群号 + true。
//
// 必须**同时**含动词关键字 + 一个 5~12 位群号才算命中——避免单纯发个"123456"或
// "我想接入"这类不完整请求被错误升级。
func parseAccessRequest(text string) (int64, bool) {
	if !reAccessVerb.MatchString(text) {
		return 0, false
	}
	m := reAccessGroupID.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0, false
	}
	gid, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || gid <= 0 {
		return 0, false
	}
	return gid, true
}

// handleGroupAccessRequest 走完"校验角色 → 上报运维群 → 回执申请人"全流程。
func handleGroupAccessRequest(client *http.Client, msg *model.Message, rawText string, targetGroup int64) {
	caller := msg.UserID
	now := time.Now()

	if !allowAccessRequest(accessRequestKey{CallerQQ: caller, TargetGroup: targetGroup}, now) {
		zaplog.Logger.Infof("群接入申请限流命中 caller=%d target=%d", caller, targetGroup)
		_, _ = SendPrivateText(client, caller,
			fmt.Sprintf("群 %d 的接入申请刚已上报，请等运营回复；5 分钟内不重复登记。", targetGroup))
		return
	}

	role := resolveRoleInGroup(client, targetGroup, caller)
	zaplog.Logger.Infof("群接入申请 caller=%d target=%d role=%s", caller, targetGroup, role)

	// 上报运维群——只用 sender.nickname（QQ 全局昵称），不取群名片。
	// 私聊场景下群名片本来也几乎不会被填，这里同时跟群消息流的展示策略对齐。
	requesterName := strings.TrimSpace(msg.Sender.Nickname)
	NotifyOpsGroupAccessRequest(client, caller, requesterName, targetGroup, role, rawText)

	// 私聊回执
	var reply string
	switch role {
	case "owner":
		reply = fmt.Sprintf("已收到：将群 %d 接入 bot 的申请；身份核验=群主，已上报运营审核。", targetGroup)
	case "admin":
		reply = fmt.Sprintf("已收到：将群 %d 接入 bot 的申请；身份核验=管理员，已上报运营审核。", targetGroup)
	case "member":
		reply = fmt.Sprintf("已收到：将群 %d 接入 bot 的申请；你不是该群管理员/群主，已上报运营复核（建议改由群主/管理员申请会更快）。", targetGroup)
	case "not_in_group":
		reply = fmt.Sprintf("已收到：将群 %d 接入 bot 的申请；bot 当前不在该群，无法核验你的身份，已上报运营人工处理（请先把 bot 拉进该群再申请）。", targetGroup)
	default:
		reply = fmt.Sprintf("已收到：将群 %d 接入 bot 的申请；身份核验暂不可用，已上报运营人工处理。", targetGroup)
	}
	if _, err := SendPrivateText(client, caller, reply); err != nil {
		zaplog.Logger.Warnf("群接入申请回执失败 caller=%d: %v（可能 caller 与 bot 非好友）", caller, err)
	}
}

// resolveRoleInGroup 走 NapCat get_group_member_info 取 caller 在 target group 的 role。
//
// 返回："owner" / "admin" / "member" / "not_in_group"（bot 不在或 caller 不在）/ "unknown"（API 错）。
func resolveRoleInGroup(client *http.Client, targetGroup, caller int64) string {
	err, resp := service.GetGroupMemberInfo(client, &model.GetGroupMemberInfoReq{
		GroupID: targetGroup,
		UserID:  caller,
		NoCache: true,
	})
	if err != nil {
		// NapCat 业务错通常是 "群不存在 / 未加入该群 / 成员不在群里"
		// 简单按 message 关键字粗判，能区分也好记录
		s := strings.ToLower(err.Error())
		switch {
		case strings.Contains(s, "群不存在"), strings.Contains(s, "not joined"),
			strings.Contains(s, "未加入"), strings.Contains(s, "no such group"):
			return "not_in_group"
		}
		zaplog.Logger.Warnf("get_group_member_info 失败 group=%d user=%d: %v", targetGroup, caller, err)
		return "unknown"
	}
	if resp == nil {
		return "unknown"
	}
	role := strings.ToLower(strings.TrimSpace(resp.Data.Role))
	switch role {
	case "owner", "admin", "member":
		return role
	}
	return "unknown"
}
