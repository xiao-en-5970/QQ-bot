// Package logic 的 auto_reply.go 实现群聊白名单"窗口聚合 + LLM 识别"。
//
// 触发流程（详见 skill/bot/SKILL.md 的"窗口聚合"段）：
//
//	NapCat WS event (group message) → handle_at_message::handleAutoReply
//	  → autoReplyMgr.Push(groupID, userID, msg)        非阻塞，仅入桶
//	  → 桶里只是把消息追加进去 + 更新 LastSeenAt
//	StartAutoReplyScanner goroutine (5s 间隔) 扫描所有桶：
//	  → 桶满（>= MaxWindowSize）or 沉默够久（now - LastSeenAt >= WindowSeconds） → flush
//	  → flush 时把整段消息丢给 Kimi 做"业务动作识别"
//	  → P0 阶段：识别到动作只 log + 群里 @ 用户回执"识别到 XXX，业务接通中"，不真调 hfut
//
// 设计要点：
//   - per-(groupID, userID) 一个独立桶 (severityKey)，互不干扰
//   - 桶在内存里，bot 重启会丢——按设计可接受（详见 skill）
//   - 写入桶用 mutex 保护，扫描走 mutex 持锁拷贝快照后释放，不长持锁调 Kimi
//   - flush 完桶就清空（不是删，避免 map key 频繁 churn；下次 Push 直接复用）
//   - bot 自己发的消息 / 已经处理过的消息 不该走到这里（HandleAtMessage 顶部已过滤）
package logic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/model"
	"qq_bot/utils/client_pool"
	"qq_bot/utils/kimi"
	zaplog "qq_bot/utils/zap"
	"strings"
	"sync"
	"time"
)

// autoReplyMsg 单条入桶的群消息（消息体的最小必要快照）。
//
// 不直接存 *model.Message：
//  1. 防止外部修改影响内存
//  2. 我们关心的就是 user_id / time / segments / 群名片，纯数据结构更好序列化给 LLM
type autoReplyMsg struct {
	MessageID  int64
	UserID     int64
	UserCard   string // 群名片 / 昵称（取自 sender.card / sender.nickname）
	Time       time.Time
	Segments   []model.MessageSegment // 原 segments（含 image url、at、text 等）
	FlatText   string                 // 扁平化的文本表示（含 [图片] 等占位符），方便 log / 喂 LLM
}

// autoReplyBucket 单个 (group, user) 的滑动窗口。
//
// LastSeenAt：最后一条消息的入桶时间（不是消息本身的 time，因为 NapCat 发回来的 time
// 可能跟服务器墙钟有偏差；用入桶时间做 silence 判定更稳）。
type autoReplyBucket struct {
	GroupID    int64
	UserID     int64
	Msgs       []autoReplyMsg
	LastSeenAt time.Time
}

// autoReplyManager 全局的窗口管理器。
//
// 所有方法都是并发安全的——Push 来自 wsclient 协程，Scan 来自专用扫描协程。
type autoReplyManager struct {
	mu      sync.Mutex
	buckets map[autoReplyBucketKey]*autoReplyBucket
}

type autoReplyBucketKey struct {
	GroupID int64
	UserID  int64
}

// 单例：跨 wsclient / scanner 共用同一份桶状态。
var autoReplyMgr = &autoReplyManager{
	buckets: make(map[autoReplyBucketKey]*autoReplyBucket),
}

// Push 把一条消息追加到对应桶。来自 handle_at_message::handleAutoReply。
//
// 不会主动 flush——flush 由扫描协程按时间/容量触发。
func (m *autoReplyManager) Push(groupID, userID int64, userCard string, msg *model.Message) {
	if msg == nil {
		return
	}
	key := autoReplyBucketKey{GroupID: groupID, UserID: userID}
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.buckets[key]
	if !ok {
		b = &autoReplyBucket{GroupID: groupID, UserID: userID}
		m.buckets[key] = b
	}
	b.Msgs = append(b.Msgs, autoReplyMsg{
		MessageID: msg.MessageID,
		UserID:    userID,
		UserCard:  userCard,
		Time:      now,
		Segments:  msg.Message,
		FlatText:  flattenMessageText(msg),
	})
	b.LastSeenAt = now

	// 桶超大时主动 flush——异常情况下用户狂发 30 条不停顿，不能一直攒。
	// 注意：Flush 内部需要拿不到 mu，这里释放后再调（用 goroutine 异步）。
	maxSize := conf.Cfg.Group.AutoReplyMaxWindowSize
	if maxSize <= 0 {
		maxSize = 20
	}
	shouldFlush := len(b.Msgs) >= maxSize

	// P3.2：消歧的"立刻 flush"路径。
	//
	// 用户在 1min 内回单字数字选择（"1"/"2"/"①"），桶里又只有这一条，就跳过
	// 5s~60s 的 silence 窗口，立刻 flush 让消歧响应秒回——选择题等 60s 才回是
	// 反人类的体验。
	//
	// 注意：消歧状态在 processSnapshot 那一层才会被消费 / 清掉；这里只是"看到
	// 状态在 + 当前消息是数字"就触发 flush，正确性由 processSnapshot 的 disambig
	// take + TTL 共同保证。
	if !shouldFlush && len(b.Msgs) == 1 {
		flat := strings.TrimSpace(b.Msgs[0].FlatText)
		if disambigChoiceFromText(flat) > 0 && disambigMgr.Get(key) != nil {
			shouldFlush = true
		}
	}

	if shouldFlush {
		// 单独抽出快照、清空桶、起 goroutine 处理；保持 Push 是 O(1) 不阻塞 wsclient
		snapshot := b.Msgs
		b.Msgs = nil
		go m.processSnapshot(key, snapshot)
	}
}

// scanOnce 扫描一遍所有桶，把已经"沉默够久"的桶 flush 出去。
//
// 持锁阶段只做"决策 + 拷贝快照 + 清空原桶"，不调 Kimi、不打网络 IO；
// 拿到快照之后释放锁再异步处理，避免长时间阻塞 Push。
func (m *autoReplyManager) scanOnce() {
	silenceSec := conf.Cfg.Group.AutoReplyWindowSeconds
	if silenceSec <= 0 {
		silenceSec = 60
	}
	threshold := time.Duration(silenceSec) * time.Second
	now := time.Now()

	type ready struct {
		key      autoReplyBucketKey
		snapshot []autoReplyMsg
	}
	var ready_ []ready

	m.mu.Lock()
	for k, b := range m.buckets {
		if len(b.Msgs) == 0 {
			continue
		}
		if now.Sub(b.LastSeenAt) < threshold {
			continue
		}
		ready_ = append(ready_, ready{key: k, snapshot: b.Msgs})
		b.Msgs = nil // 清空已 flush 的部分
	}
	m.mu.Unlock()

	for _, r := range ready_ {
		m.processSnapshot(r.key, r.snapshot)
	}
}

// processSnapshot P0 阶段的"识别 + 回执"逻辑。
//
// 流程：
//  1. 把 snap 转成 RecognizeInput（每条消息只保留 message_id / time / segments 文本表示）
//  2. 调 Kimi 的 RecognizeBusinessActions，强制 JSON output，拿到 actions 数组
//  3. 对每个 type != "none" 的 action 在群里 @ 用户回执"识别到 XXX，业务对接中"
//  4. **P0 阶段不真调 hfut**——回执文本里带 [识别测试] 前缀，让群友也知道是测试
//
// P1 阶段会把 hfut 客户端接进来，回执文案改成正式版本，并真正调 publish_good / off_shelf。
func (m *autoReplyManager) processSnapshot(key autoReplyBucketKey, snap []autoReplyMsg) {
	if len(snap) == 0 {
		return
	}
	first := snap[0]
	last := snap[len(snap)-1]
	zaplog.Logger.Infof("autoReply flush group=%d user=%d card=%q msgs=%d duration=%s",
		key.GroupID, key.UserID, first.UserCard, len(snap), last.Time.Sub(first.Time))
	for i, msg := range snap {
		zaplog.Logger.Infof("  [%d] msgid=%d %s | %s",
			i+1, msg.MessageID, msg.Time.Format("15:04:05"), truncateForLog(msg.FlatText, 200))
	}

	// P3.2：消歧选择消费——如果当前 (group, user) 有 pending disambig 上下文，
	// 且**整段窗口里的所有消息**都是单字数字选择（"1"/"2"/"①"），就直接处理选择
	// 而不送 Kimi。同窗口里夹了别的话题（"1 还有这个鞋架也卖 5 块"）就忽略消歧、
	// 走正常识别路径，让 Kimi 处理新意图。
	//
	// 这里读上下文用 Get（不删）——只有真正消费完 hfut 调用后再 Take/Clear；
	// 避免"网络抖动 → choice 失败 → 状态丢" 让用户没法重试。但实测中失败也不该
	// 让用户多按一次（设计上消歧是一次性的），所以失败时也 Take。
	if pending := disambigMgr.Get(key); pending != nil {
		if choice := windowAsDisambigChoice(snap); choice > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			httpClient := client_pool.NewClientPool()
			c := disambigMgr.Take(key) // 消费即删
			res := dispatchDisambigChoice(ctx, c, choice)
			verbose := conf.Cfg.Group.IsAutoReplyVerbose()
			if res.shouldEmit(verbose) {
				zaplog.Logger.Infof("autoReply 消歧 ack → group=%d user=%d kind=%d: %s",
					key.GroupID, key.UserID, res.Kind, truncateForLog(res.Text, 200))
				_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
			}
			return
		}
		// 窗口里不是单纯的数字选择 = 用户改话题了，放弃消歧（让 TTL 自然过期也行，
		// 这里显式 Clear 避免污染下次窗口）
		disambigMgr.Clear(key)
	}

	if global.Kimi == nil {
		zaplog.Logger.Debugf("autoReply group=%d user=%d Kimi 未启用，跳过识别", key.GroupID, key.UserID)
		return
	}

	// 调 Kimi 识别。给一个相对宽的超时——单次 moonshot completions 通常几秒内回，60s 兜底。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	input := buildRecognizeInput(key, first.UserCard, snap)
	result, err := global.Kimi.RecognizeBusinessActions(ctx, input)
	switch {
	case err == nil:
		// 正常路径——result 来自 Kimi
	case errors.Is(err, kimi.ErrQuotaCooling):
		// quota gate 冷却期：退化到 regex 兜底（保守识别，仅 publish_good + off_shelf）
		zaplog.Logger.Infof("autoReply group=%d user=%d Kimi quota 冷却中 → regex 兜底识别",
			key.GroupID, key.UserID)
		result = kimi.RecognizeViaRegex(input)
	case kimi.IsQuotaError(err):
		// 单次 quota error——quota_gate 还没累积到阈值（默认连续 3 次才正式冷却），但当前
		// 这条消息不该白白被 drop。立即走 regex 兜底；quota_gate 会持续累计，到阈值后正式
		// short-circuit 后续 API 调用。
		// 日志降级 ERROR → WARN，避免运行时配额耗尽时刷屏。
		zaplog.Logger.Warnf("autoReply group=%d user=%d Kimi 配额耗尽（单次） → regex 兜底识别（请尽快充值 / 换 API key）",
			key.GroupID, key.UserID)
		result = kimi.RecognizeViaRegex(input)
	default:
		zaplog.Logger.Errorf("autoReply 识别失败 group=%d user=%d: %v", key.GroupID, key.UserID, err)
		return
	}
	if len(result.Actions) == 0 {
		zaplog.Logger.Infof("autoReply group=%d user=%d 识别结果: 无业务动作", key.GroupID, key.UserID)
		return
	}

	// 单独造一个 client 用来发回执，避免跟其它协程争用
	httpClient := client_pool.NewClientPool()

	for i, a := range result.Actions {
		zaplog.Logger.Infof("autoReply group=%d user=%d action[%d] type=%s confidence=%.2f reason=%q",
			key.GroupID, key.UserID, i, a.Type, a.Confidence, a.Reason)
		if a.Type == "none" {
			continue
		}

		// 选回执文案 + 等级：
		//   - hfut 已配置 → 真调 hfut（内部 upsert 旗下账号 + 落库），返回 ackResult{text,kind}
		//   - hfut 未配置 → P0 占位 ack（[识别测试] xxx 暂未真发布），等级当 success 处理
		var res ackResult
		if global.Hfut != nil {
			res = dispatchActionToHfut(ctx, key, first.UserCard, snap, a)
		} else {
			// 占位 ack 在 verbose 下也只是给开发看，按 fail 处理——这样 normal 模式
			// 跑没接 hfut 的 bot 会保持完全静默（也是合理的）
			res = ackResult{Text: buildAckMessage(a), Kind: ackKindFail}
		}

		// 按 verbosity 决定要不要真发到群里
		verbose := conf.Cfg.Group.IsAutoReplyVerbose()
		if !res.shouldEmit(verbose) {
			zaplog.Logger.Infof("autoReply ack 抑制 group=%d user=%d kind=%d verbose=%v text=%q",
				key.GroupID, key.UserID, res.Kind, verbose, truncateForLog(res.Text, 100))
			continue
		}
		zaplog.Logger.Infof("autoReply ack → group=%d user=%d kind=%d: %s",
			key.GroupID, key.UserID, res.Kind, truncateForLog(res.Text, 200))
		_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, res.Text)
	}
}

// buildRecognizeInput 把窗口快照转成喂给 Kimi 的 RecognizeInput。
//
// 文本段：直接抄 td.Text；
// 图片段：用 "[图片]" 占位符——具体 URL 暂不交给模型，避免 URL 干扰判断（模型只需要知道
//
//	"这条消息有图"以及它的 message_id）；
// 其它段（at / face / reply 等）：用 "[type:X]" 占位符。
func buildRecognizeInput(key autoReplyBucketKey, userCard string, snap []autoReplyMsg) kimi.RecognizeInput {
	msgs := make([]kimi.RecognizeMsg, 0, len(snap))
	for _, m := range snap {
		segs := make([]string, 0, len(m.Segments))
		for _, seg := range m.Segments {
			switch seg.Type {
			case "text":
				if td, err := model.AsTextData(seg.Data); err == nil {
					segs = append(segs, td.Text)
				}
			case "image":
				segs = append(segs, "[图片]")
			case "at":
				segs = append(segs, "[at]")
			case "face":
				segs = append(segs, "[表情]")
			case "reply":
				segs = append(segs, "[引用回复]")
			default:
				segs = append(segs, "["+seg.Type+"]")
			}
		}
		msgs = append(msgs, kimi.RecognizeMsg{
			MessageID: m.MessageID,
			Time:      m.Time.Format("15:04:05"),
			Segments:  segs,
		})
	}
	return kimi.RecognizeInput{
		GroupID:  key.GroupID,
		UserID:   key.UserID,
		UserCard: userCard,
		Messages: msgs,
	}
}

// buildAckMessage 把识别到的 RecognizeAction 翻成一条群里 @ 用户的回执文本。
//
// P0 阶段所有回执都加 [识别测试] 前缀，明确标注"暂未真发布"——避免群友以为已经上架了去找。
// P1 阶段把 [识别测试] 删掉、把"暂未真发布"改成实际的 hfut 商品 ID 链接。
func buildAckMessage(a kimi.RecognizeAction) string {
	const prefix = "[识别测试]"
	switch a.Type {
	case "publish_good":
		category := "二手"
		if a.Category == 2 {
			category = "有偿求助"
		}
		var b strings.Builder
		b.WriteString(prefix)
		b.WriteString(" 检测到你想上架")
		b.WriteString(category)
		b.WriteString("「")
		b.WriteString(orPlaceholder(a.Title, "(无标题)"))
		b.WriteString("」")
		if a.Negotiable || a.Price == nil {
			b.WriteString(" 价格面议")
		} else {
			b.WriteString(fmt.Sprintf(" 价格 %g 元", *a.Price))
		}
		if a.Location != "" {
			b.WriteString(" 地点 ")
			b.WriteString(a.Location)
		}
		if len(a.ImageMessageIDs) > 0 {
			b.WriteString(fmt.Sprintf(" 图×%d", len(a.ImageMessageIDs)))
		}
		b.WriteString("，业务对接中（暂未真发布）")
		return b.String()

	case "publish_question":
		return fmt.Sprintf("%s 检测到你想发起提问「%s」，业务对接中（暂未真发布）",
			prefix, orPlaceholder(a.QuestionTitle, "(未识别标题)"))

	case "publish_answer":
		hint := orPlaceholder(a.AnswerHintTo, "(未识别针对哪条提问)")
		return fmt.Sprintf("%s 检测到你想回答「%s」，业务对接中（暂未真发布）", prefix, hint)

	case "off_shelf":
		hint := orPlaceholder(a.OffShelfHint, "(未指明，多个在售时会反问)")
		return fmt.Sprintf("%s 检测到你想下架「%s」，业务对接中（暂未真下架）", prefix, hint)

	case "close_question":
		hint := orPlaceholder(a.CloseQuestionHint, "(未指明)")
		return fmt.Sprintf("%s 检测到你想关闭提问「%s」，业务对接中（暂未真关闭）", prefix, hint)

	default:
		return ""
	}
}

// windowAsDisambigChoice 检查整段窗口是否仅是"用户的一次消歧选择"——返回 1-based
// index，0 表示不是消歧选择（应当走正常识别路径）。
//
// 规则：
//   - 必须只有 1 条消息（多条 → 用户在窗口里讲了别的话题，不算消歧）
//   - 该消息的 FlatText 必须能被 disambigChoiceFromText 解析为 1~5
//
// 调用方应当**先**确认 disambigMgr.Get(key) != nil（有 pending 上下文）才调本函数；
// 否则单独发"1"会被误识别。
func windowAsDisambigChoice(snap []autoReplyMsg) int {
	if len(snap) != 1 {
		return 0
	}
	return disambigChoiceFromText(snap[0].FlatText)
}

func orPlaceholder(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// StartAutoReplyScanner 启动后台扫描协程。
//
// 调用约定：必须由 main 在 `go StartAutoReplyScanner(ctx)` 之前调用 global.Wg.Add(1)，
// 跟其它后台协程同源（避免之前那个 Wg.Wait race）。
//
// 实现：5s 一次 tick，每次扫一遍 buckets 决定 flush。
// 5s 这个周期相对于 60s 沉默窗口足够细，不会让 flush 多延迟太多。
func StartAutoReplyScanner(ctx context.Context, _ *http.Client) {
	defer global.Wg.Done()
	zaplog.Logger.Debugf("协程AutoReplyScanner启动")
	defer zaplog.Logger.Debugf("协程AutoReplyScanner退出")

	t := time.NewTicker(5 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			autoReplyMgr.scanOnce()
		}
	}
}
