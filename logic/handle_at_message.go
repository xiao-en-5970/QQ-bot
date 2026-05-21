package logic

import (
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/model"
	zaplog "qq_bot/utils/zap"
	"strconv"
	"strings"
)

// HandleAtMessage 处理一条 OneBot11 群消息事件，分两条路径：
//
//  1. **@bot 命令路径**：消息 `segment[0].Type == "at"` 且 `at.qq == bot_id` →
//     按 commands.enabled 白名单走 ExecCmd 链路（jm / pix / chat 等子命令）。
//
//  2. **群聊自动回复路径**：消息没 @bot，但所在群在 `conf.Cfg.Group.AutoReplyWhitelist` 里 →
//     交给 handleAutoReply（占位实现）；具体回复策略由 bot skill 决定（见 skill/bot/SKILL.md），
//     当前只做日志记录，等业务逻辑明确再展开。
//
//  3. 其它情况（没 @bot 且群不在白名单）→ 直接忽略，bot 完全不响应。
//
// 这个函数是 utils/wsclient 收到 NapCat 推送的 group message 事件时的唯一入口。
//
// 防重复 / 防回响（两条路径共用）：
//  1. bot 自己发的消息直接跳过（防 bot 回复自己 / 防止白名单模式下 bot 把自己的消息当
//     聊天上下文又触发一次回复）
//  2. message_id 走全局 LRU（global.ProcessedMsgIDs）去重——WS 单连接下 NapCat 不会重复
//     推同一事件，但断线重连 / 跑两个 bot 实例时仍能兜底
func HandleAtMessage(client *http.Client, msg *model.Message) {
	if msg == nil || msg.GroupID == 0 {
		return
	}

	if conf.Cfg.User.UserID == nil {
		zaplog.Logger.Errorf("HandleAtMessage 收到事件但 bot user_id 未初始化，丢弃 msgid=%d", msg.MessageID)
		return
	}
	botID := *conf.Cfg.User.UserID

	// 防回响：bot 自己发的消息直接跳过。NapCat 默认会把"已发送的群消息"也作为
	// post_type=message_sent 推回来；wsclient 在源头已经过滤了 message_sent，但
	// 如果未来配置打开 reportSelfMessage，这里也得兜住。
	if msg.UserID == botID {
		return
	}

	// LRU 防重处理同一 message_id 的边界场景（断线重连 / 多实例）
	if !global.ProcessedMsgIDs.Add(msg.MessageID) {
		zaplog.Logger.Debugf("group=%d msgid=%d 已处理过，跳过(防重复)", msg.GroupID, msg.MessageID)
		return
	}

	if len(msg.Message) == 0 {
		return
	}

	// 判断是不是 @bot —— 仅认 segment[0] 是 at 且 qq 等于 botID。
	// at.qq=="all"（@全体）不算 @bot，会落到自动回复路径或被忽略。
	isAtBot := false
	if first := msg.Message[0]; first.Type == "at" {
		if atData, _ := model.AsAtData(first.Data); atData.QQ != "all" {
			if atID, parseErr := strconv.ParseInt(atData.QQ, 10, 64); parseErr == nil && atID == botID {
				isAtBot = true
			}
		}
	}

	if isAtBot {
		// 运维群里的 @bot 走"自然语言查询"路径——bot 调 LLM 生 SQL → 调 hfut 执行 → 回结果。
		// 普通命令分发只在非运维群（或 OpsGroup 未配置时）启用，避免运维群里写 jm/pix 等
		// 跟运维查询语义混淆。详见 logic/ops_query.go 与 skill/bot/ops_query.md。
		if conf.Cfg.Bot.IsOpsGroup(msg.GroupID) {
			text := extractFirstTextAfterAt(msg)
			HandleOpsQuery(client, msg, text)
			return
		}
		handleAtCommand(client, msg, botID)
		return
	}

	// 非 @bot：仅当群在白名单时才走自动回复路径
	if conf.Cfg.Group.IsAutoReplyGroup(msg.GroupID) {
		handleAutoReply(client, msg)
		return
	}

	// 不在白名单 + 不是 @bot → 完全忽略
}

// handleAtCommand 走原本的 @bot 命令链路：识别 segment[1] 文本、丢入 ChanToParseCmd。
//
// 拆出独立函数是为了让 HandleAtMessage 顶部的分流（@bot vs 自动回复）保持清晰可读。
func handleAtCommand(client *http.Client, msg *model.Message, botID int64) {
	_ = botID // 当前函数内部不需要再用 botID，但留参数让调用关系显式
	// 命令路径需要 commands.enabled 至少有一项；全禁用时连菜单也不发，跟"严格 opt-in"语义一致。
	if !conf.Cfg.Commands.AnyEnabled() {
		zaplog.Logger.Debugf("group=%d msgid=%d commands.enabled 为空，bot 不响应任何命令",
			msg.GroupID, msg.MessageID)
		return
	}

	if len(msg.Message) == 1 {
		zaplog.Logger.Debugf("group=%d msgid=%d 单独 @bot, return menu", msg.GroupID, msg.MessageID)
		// 全禁用时 Menu() 返回 ""，这里不发任何东西
		if menu := global.Menu(); menu != "" {
			_ = SendGroupAtText(client, msg.GroupID, msg.UserID, menu)
		}
		return
	}

	second := msg.Message[1]
	if second.Type != "text" {
		zaplog.Logger.Debugf("group=%d msgid=%d @bot 后非文本 (type=%s)，return menu",
			msg.GroupID, msg.MessageID, second.Type)
		_ = SendGroupAtText(client, msg.GroupID, msg.UserID, global.ErrCmdArgFault)
		return
	}
	td, decErr := model.AsTextData(second.Data)
	if decErr != nil {
		zaplog.Logger.Errorf("解析 text segment 失败: %v", decErr)
		return
	}
	text := strings.TrimSpace(td.Text)
	if text == "" {
		return
	}

	zaplog.Logger.Infof("group=%d msgid=%d user=%d 收到指令: %s",
		msg.GroupID, msg.MessageID, msg.UserID, text)

	// 阻塞 send 是 by design 的反压：ChanToParseCmd(15) + ants pool(20) 满了会让本协程
	// 在这里短暂等待，等 ParseCmd/worker 消化完自然恢复。
	global.ChanToParseCmd <- model.ChanToParseCmd{
		GroupID: msg.GroupID,
		UserID:  msg.UserID,
		Data:    model.TextData{Text: text},
	}
}

// handleAutoReply 处理白名单群的"非 @bot"消息：把消息推入 per-(group,user) 滑动窗口。
//
// 完整策略见 skill/bot/SKILL.md 的"窗口聚合"段。这里只做一件事——入桶；
// 真正的 LLM 识别 / 回执 / 上架在 logic/auto_reply.go 的扫描协程里做。
//
// 入桶是 O(1) 操作（map + slice append + mutex），不会阻塞 wsclient 协程。
//
// 特例：消息里含 forward segment（"聊天记录"合并转发）时——单独走 expandAndPushForward：
// 调 NapCat /get_forward_msg 拿子节点，把每条子消息作为转发者的伪消息按顺序 Push
// 进同一桶，让聊天记录里的上架文字 + 配图能被现有窗口聚合 / 三态切分正常识别。
// 这条网络 IO 不能在 wsclient 协程里同步做，所以放到独立 goroutine。
func handleAutoReply(client *http.Client, msg *model.Message) {
	_ = client // 当前不在入桶阶段调用 client；扫描协程会自带一份 client 用来发回执

	if containsForwardSegment(msg) {
		go expandAndPushForward(msg)
		return
	}

	// 展示名**只**使用 sender.nickname（QQ 全局昵称），**绝对不**回退到 sender.card（群名片）。
	//
	// 为什么不允许群名片：app 端 author 卡片 / 个人展示页要展示用户的"QQ 身份"，
	// 同一 QQ 在不同群可能被设不同备注，跨群展示会出现"明明是同一个人，名字却不一样"
	// 的违和感。群名片只是这个群的本地化备注，不是这个人的真实身份。
	//
	// sender.nickname 罕见为空的情况：bot 不上报展示名，hfut 端收到空串不会覆盖现有
	// nickname；同时 scheduler 每 6h 通过 get_stranger_info 主动拉 QQ 全局昵称兜底。
	displayName := strings.TrimSpace(msg.Sender.Nickname)
	autoReplyMgr.Push(msg.GroupID, msg.UserID, displayName, msg)
}

// extractFirstTextAfterAt 提取 @bot 之后的纯文本——给运维群 @ 提问用。
//
// 跟 handleAtCommand 不同：
//   - handleAtCommand 只看 segment[1] 一个 text；
//   - 这里把 segment[1:] 所有 text 拼起来，让运维写较长的问题不会被吞。
//
// 非 text 段（image/face 等）忽略；@ 段因为我们假设第一个是 @bot，后续若有再被 @ 的人
// 也直接展开成 "@QQ" 文本（不影响 LLM 理解）。
func extractFirstTextAfterAt(msg *model.Message) string {
	if msg == nil || len(msg.Message) <= 1 {
		return ""
	}
	var b strings.Builder
	for _, seg := range msg.Message[1:] {
		switch seg.Type {
		case "text":
			if td, err := model.AsTextData(seg.Data); err == nil {
				b.WriteString(td.Text)
			}
		case "at":
			if ad, err := model.AsAtData(seg.Data); err == nil {
				b.WriteString("@")
				b.WriteString(ad.QQ)
				b.WriteByte(' ')
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// flattenMessageText 把 message segments 里所有 text 拼成一条字符串，方便 log / 传给后续逻辑。
// at / image / face 等非文本段统一用占位符表示，保留语义大概。
func flattenMessageText(msg *model.Message) string {
	if msg == nil || len(msg.Message) == 0 {
		return ""
	}
	var b strings.Builder
	for _, seg := range msg.Message {
		switch seg.Type {
		case "text":
			if td, err := model.AsTextData(seg.Data); err == nil {
				b.WriteString(td.Text)
			}
		case "at":
			if ad, err := model.AsAtData(seg.Data); err == nil {
				b.WriteString("@")
				b.WriteString(ad.QQ)
				b.WriteString(" ")
			}
		case "image":
			b.WriteString("[图片]")
		case "face":
			b.WriteString("[表情]")
		case "reply":
			b.WriteString("[引用回复]")
		case "forward":
			// forward 段正常会在 handleAutoReply 入口被 expandAndPushForward 异步展开；
			// 走到这里只可能是"fetch 失败兜底"分支——保留一个明显的占位符让 Kimi 知道
			// 原消息里有合并转发存在，但内容拿不到。
			b.WriteString("[合并转发(未展开)]")
		default:
			b.WriteString("[" + seg.Type + "]")
		}
	}
	return strings.TrimSpace(b.String())
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
