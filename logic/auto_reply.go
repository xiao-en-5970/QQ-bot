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
//
//	「不等 60s」的特例（仍为单桶单用户）：
//	  - 待消歧时回 1/2/3——立刻 flush（原 P3.2）
//	  - 去重提示后回「下架旧的」且有待下架 follow-up——立刻 flush，`processDupOffShelfAck` 里直接 OffShelf
//	  - 单条纯文字、无图、正则与关键词都不像上架/下架/求助——立刻丢弃桶，静默（不调 Kimi、不占满 silence）
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
	"qq_bot/utils/metrics"
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
	MessageID int64
	UserID    int64
	UserCard  string                 // 用户展示名——**只**取 sender.nickname（QQ 全局昵称），不允许群名片污染
	Time      time.Time              // 入桶时间——给"滑动窗口裁剪 / silence 计时"用
	Segments  []model.MessageSegment // 原 segments（含 image url、at、text 等）
	FlatText  string                 // 扁平化的文本表示（含 [图片] 等占位符），方便 log / 喂 LLM
	// OriginTime 这条消息的"逻辑时间"——给 buildRecognizeInput 优先用，显示真实发送
	// 时间让 Kimi 看到节点间真实间隔。零值时 buildRecognizeInput 会 fallback 到 Time。
	//
	// 为什么不直接用 Time：Time 还要给滑动窗口裁剪用——如果让"聊天记录里 1 小时前
	// 的 node"伪装成 1 小时前入桶，滑动窗口会立刻把它当过期消息扔掉，达不到展开
	// 喂 Kimi 的目的。所以拆成 Time（实际入桶）+ OriginTime（语义层时间）两个字段。
	OriginTime time.Time
	// FromForwardChat 这条消息是不是"聊天记录展开后的子消息"。
	// true 时 buildRecognizeInput 会在 segments 开头加 [来自聊天记录] 占位，让 Kimi
	// 知道这条原本是用户**过去某时刻独立发布**的——不要跟其它消息合并成同一个商品。
	// 详见 forward_expand.go::expandAndPushForward。
	FromForwardChat bool
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
	m.pushInternal(groupID, userID, userCard, msg, time.Time{}, false, false)
}

// PushFromForward 跟 Push 同义，但额外标记"这条来自聊天记录展开后的子消息"，
// 并指定逻辑时间（node 在源聊天里的发送时间，秒级 unix）。
//
// 用途：handleAutoReply 检测到 forward 段时把整段消息异步展开成 N 条伪消息，
// 用本函数 push 进同一桶。Kimi prompt 据此判定"每条独立判定 / 不合并"。
//
// originTime 为零值时 fallback 到 time.Now()（节点没带 time 字段时用得上）。
//
// **注意**：本函数会**触发** maxSize flush——只适合"零散补 push"场景。如果是
// 一次性 push 一整段聊天记录（>20 节点），用 PushBatchFromForward 整批一次过，
// 避免被 20 条上限切成多个 snapshot。
func (m *autoReplyManager) PushFromForward(groupID, userID int64, userCard string, msg *model.Message, originTime time.Time) {
	m.pushInternal(groupID, userID, userCard, msg, originTime, true, false)
}

// PushBatchFromForward 一次性 push 一整段聊天记录展开后的所有伪消息。
//
// 跟"循环里调 PushFromForward"的关键差别：本函数**整批跳过 maxSize flush 检查**，
// 哪怕一次塞进 50 条也不会在第 20 条时被切碎。整段聊天记录就该作为一个 snapshot
// 整体送给 Kimi 识别成"批量上架"商品（IsBatch=true）。
//
// 入参 msgs 和 originTimes 长度必须相等且一一对应；任一为 0 长度直接 noop。
func (m *autoReplyManager) PushBatchFromForward(groupID, userID int64, userCard string,
	msgs []*model.Message, originTimes []time.Time) {
	if len(msgs) == 0 || len(msgs) != len(originTimes) {
		return
	}
	for i, msg := range msgs {
		m.pushInternal(groupID, userID, userCard, msg, originTimes[i], true, true /* suppressSizeFlush */)
	}
}

func (m *autoReplyManager) pushInternal(groupID, userID int64, userCard string,
	msg *model.Message, originTime time.Time, fromForward bool, suppressSizeFlush bool) {
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
		MessageID:       msg.MessageID,
		UserID:          userID,
		UserCard:        userCard,
		Time:            now, // 入桶时间——给窗口裁剪用，统一是 now
		OriginTime:      originTime,
		Segments:        msg.Message,
		FlatText:        flattenMessageText(msg),
		FromForwardChat: fromForward,
	})
	b.LastSeenAt = now

	// 顺带把这位 QQ 用户的 nickname / 头像同步到 hfut——
	// 30 分钟节流 + fire-and-forget，让旗下号 nickname 跟着群消息逐步回填
	// （即便用户没触发业务动作）。详见 display_sync.go。
	maybeSyncQQDisplay(groupID, userID, userCard)

	// 60s 滑动窗口：丢弃桶里超过 windowSeconds 之前的旧消息。
	//
	// 跟 silence 是不同概念——silence 看"最近一条消息以来的静默时间"决定 flush 时机；
	// 滑动窗口看"每条消息自身的年龄"决定它是否还在视野里。目的是防止"用户半小时前发
	// 的图 + 现在发的文字"被识别成同一上架，引入跨上下文误判。
	windowSec := conf.Cfg.Group.AutoReplyWindowSeconds
	if windowSec <= 0 {
		windowSec = 60
	}
	cutoff := now.Add(-time.Duration(windowSec) * time.Second)
	keepFrom := 0
	for i, am := range b.Msgs {
		if !am.Time.Before(cutoff) {
			keepFrom = i
			break
		}
		keepFrom = i + 1
	}
	if keepFrom > 0 {
		b.Msgs = b.Msgs[keepFrom:]
	}

	// 桶超大时主动 flush——异常情况下用户狂发 30 条不停顿，不能一直攒。
	// 注意：Flush 内部需要拿不到 mu，这里释放后再调（用 goroutine 异步）。
	//
	// suppressSizeFlush=true 时跳过：PushBatchFromForward 一次性把一段聊天记录
	// 展开的所有节点 push 进桶（可能 >20 条），整段应作为单一 snapshot 给 Kimi
	// 识别成批量上架，不能在中间被切碎。等 silence 60s 后由 scanner 自然 flush。
	maxSize := conf.Cfg.Group.AutoReplyMaxWindowSize
	if maxSize <= 0 {
		maxSize = 20
	}
	shouldFlush := !suppressSizeFlush && len(b.Msgs) >= maxSize

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
		if !shouldFlush && dupOffShelfMgr.Peek(key) != nil && matchesDupOffShelfReply(flat) != dupChoiceNone {
			shouldFlush = true
		}
	}

	if shouldFlush {
		// 单独抽出快照、清空桶、起 goroutine 处理；保持 Push 是 O(1) 不阻塞 wsclient
		snapshot := b.Msgs
		b.Msgs = nil
		go m.processSnapshot(key, snapshot)
		return
	}

	// 不再做 unit 切分立即 flush：所有消息攒在桶里，等 60s 沉默触发后 scanOnce 整桶
	// 一次性送 Kimi 识别。
	//
	// 这是一次设计权衡：早期版本会按"图 图 图 文 → 立即闭合"的模式提前 flush，让用户
	// 上架后秒回执；但实际场景里用户在一次发布中会图文交杂（"图 图 图 视频 商品描述
	// 长文 图 价格"），早期切分会在第一段文字处截止，把后续的价格段当成新一次上架
	// 识别——经常导致价格丢失。
	//
	// 现在的取舍：所有消息等 60s 整体识别，回执延迟一点；让 Kimi 自己处理"多商品
	// + 图文交杂"的归一（详见 recognizeSystemPrompt 里"图文交杂多商品"那节）。
	// disambig / dup_off_shelf 这两个用户回数字的场景仍立即 flush（上面已处理），
	// 不被这条规则影响。
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
		key       autoReplyBucketKey
		snapshot  []autoReplyMsg
		imageOnly bool // true = 纯图 tail，走 processImageOnlySnapshot 逐图 vision OCR
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
		// Silence 触发：检查 tail 是否含业务文字（text 消息）。
		//
		//   - 含 text：模式 B 半成品（`文 图*`）正常 flush 走 Kimi 识别
		//   - 不含 text：纯图序列——交给 **vision OCR 单图分发** 路径，每张图
		//     单独调一次 Moonshot vision API，按图上的 OCR 文本独立上架。
		//
		// 这是新切分逻辑的兜底：Push 时已经把"完整 unit"切走立即 flush 了，剩在桶里
		// 的 tail 要么是含 text 的模式 B 半成品，要么是孤立的纯图。后者以前直接
		// 静默清空，现在改成走 OCR——很多用户用美图秀秀加水印标价后直接发图，
		// 不补任何文字，OCR 就能把"还剩一半 3 元" / "除螨喷雾只用了两次 3 元"
		// 这种水印识别出来上架。
		if !bucketHasMeaningfulText(b.Msgs) {
			ready_ = append(ready_, ready{key: k, snapshot: b.Msgs, imageOnly: true})
			b.Msgs = nil
			continue
		}
		ready_ = append(ready_, ready{key: k, snapshot: b.Msgs})
		b.Msgs = nil // 清空已 flush 的部分
	}
	m.mu.Unlock()

	for _, r := range ready_ {
		if r.imageOnly {
			m.processImageOnlySnapshot(r.key, r.snapshot)
			continue
		}
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

	if processDupOffShelfAck(key, snap) {
		return
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

	// 调 Kimi 识别 + 后续整段 dispatch（含图片转存 + 落库）共享 ctx。
	// 给 180s 兜底——单次 moonshot completions 通常几秒，但批量上架可能涉及
	// 20+ 张图需要并发下载 + 上传到 OSS（mirrorImagesToHfut），最坏情况叠加耗时
	// 接近 1min；60s 不够，180s 给批量场景足够余量。
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	input := buildRecognizeInput(key, first.UserCard, snap)
	result, err := global.Kimi.RecognizeBusinessActions(ctx, input)
	switch {
	case err == nil:
		// 正常路径——result 来自 Kimi
		metrics.IncRecognize("success")
	case errors.Is(err, kimi.ErrQuotaCooling):
		// quota gate 冷却期：直接静默（正则做语义识别不可靠，宁可漏不可错）
		metrics.IncRecognize("quota_cooling")
		zaplog.Logger.Infof("autoReply group=%d user=%d Kimi quota 冷却中，静默丢弃当前窗口",
			key.GroupID, key.UserID)
		return
	case kimi.IsQuotaError(err):
		// 单次 quota error——quota_gate 持续累计到阈值后会进入正式冷却。当前这条消息直接静默
		// 不做识别——配额耗尽该停就停，避免任何低置信度兜底污染落库数据。
		metrics.IncRecognize("quota_cooling")
		zaplog.Logger.Warnf("autoReply group=%d user=%d Kimi 配额耗尽（单次），静默丢弃当前窗口（请尽快充值 / 换 API key）",
			key.GroupID, key.UserID)
		return
	default:
		metrics.IncRecognize("fail")
		zaplog.Logger.Errorf("autoReply 识别失败 group=%d user=%d: %v", key.GroupID, key.UserID, err)
		return
	}
	if len(result.Actions) == 0 {
		zaplog.Logger.Infof("autoReply group=%d user=%d 识别结果: 无业务动作", key.GroupID, key.UserID)
		return
	}

	// 单独造一个 client 用来发回执，避免跟其它协程争用
	httpClient := client_pool.NewClientPool()

	// 限流：整个 snapshot 共用 1 个令牌——用户一次发 N 商品时，Kimi 会返回 N 个
	// publish_good，但这是"同一次用户行为"，不应被算 N 次。dispatchActionToHfut
	// 入参 skipRateLimit=true 跳过 per-action 计数。
	//
	// 触发限流时仅发一条 ack 给用户（不是 N 条），然后整批静默丢弃。
	hasMutating := false
	for _, a := range result.Actions {
		if isMutatingAction(a.Type) {
			hasMutating = true
			break
		}
	}
	if hasMutating {
		if ok, retry := dispatchLimiter.Allow(key); !ok {
			metrics.IncRateLimit()
			zaplog.Logger.Warnf("autoReply 限流命中 group=%d user=%d snapshot_actions=%d retry=%s",
				key.GroupID, key.UserID, len(result.Actions), retry)
			text := fmt.Sprintf("太快，%d 秒后再发", int(retry.Seconds()))
			res := ackResult{Text: text, Kind: ackKindAskUser}
			if res.shouldEmit(conf.Cfg.Group.IsAutoReplyVerbose()) {
				_ = SendGroupAtText(httpClient, key.GroupID, key.UserID, text)
			}
			return
		}
	}

	for i, a := range result.Actions {
		// 打全 title + image_message_ids + source_message_ids，方便事后复盘
		// "Kimi 把哪几张图归到哪个商品"——尤其多商品 / 批量上架场景
		zaplog.Logger.Infof("autoReply group=%d user=%d action[%d] type=%s confidence=%.2f title=%q price=%v images=%v sources=%v reason=%q",
			key.GroupID, key.UserID, i, a.Type, a.Confidence,
			a.Title, formatPrice(a.Price), a.ImageMessageIDs, a.SourceMessageIDs, a.Reason)
		if a.Type == "none" {
			continue
		}

		// 选回执文案 + 等级：
		//   - hfut 已配置 → 真调 hfut（内部 upsert 旗下账号 + 落库），返回 ackResult{text,kind}
		//   - hfut 未配置 → P0 占位 ack（[识别测试] xxx 暂未真发布），等级当 success 处理
		var res ackResult
		if global.Hfut != nil {
			res = dispatchActionToHfut(ctx, key, first.UserCard, snap, a, true /* skipRateLimit */)
		} else {
			// 占位 ack 在 verbose 下也只是给开发看，按 fail 处理——这样 normal 模式
			// 跑没接 hfut 的 bot 会保持完全静默（也是合理的）
			res = ackResult{Text: buildAckMessage(a), Kind: ackKindFail}
		}
		// 计入运维指标：按 (action.Type, ackKind) 维度分桶，方便面板看上架成功 / 重复 / 失败比例
		var outcome string
		switch res.Kind {
		case ackKindSuccess:
			outcome = "success"
		case ackKindDup:
			outcome = "dup"
		case ackKindAskUser:
			outcome = "ask_user"
		case ackKindFail:
			outcome = "fail"
		case ackKindIgnore:
			outcome = "ignore"
		default:
			outcome = "other"
		}
		metrics.IncDispatch(a.Type, outcome)
		// 写最近事件（运维面板"事件流"展示）：标题 / 价格 / 置信度 / 模型 reason 一并保留，
		// 便于事后复盘"为什么把这条群消息识别成上架"。
		title := strings.TrimSpace(a.Title)
		if title == "" {
			title = strings.TrimSpace(a.SeekHint)
		}
		if title == "" {
			title = strings.TrimSpace(a.QuestionTitle)
		}
		var price float64
		if a.Price != nil {
			price = *a.Price
		}
		metrics.RecordDispatchEvent(metrics.RecordDispatchInput{
			GroupID:    key.GroupID,
			UserID:     key.UserID,
			UserCard:   first.UserCard,
			ActionType: a.Type,
			Outcome:    outcome,
			Title:      title,
			Category:   a.Category,
			Negotiable: a.Negotiable,
			Price:      price,
			Confidence: a.Confidence,
			Reason:     a.Reason,
			AckText:    truncateForLog(res.Text, 200),
		})

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
//
// 其它段（at / face / reply 等）：用 "[type:X]" 占位符。
func buildRecognizeInput(key autoReplyBucketKey, userCard string, snap []autoReplyMsg) kimi.RecognizeInput {
	msgs := make([]kimi.RecognizeMsg, 0, len(snap))
	for _, m := range snap {
		segs := make([]string, 0, len(m.Segments)+1)
		// FromForwardChat=true：在 segments 开头标"[来自聊天记录]"，让 Kimi 知道
		// 这条原本是用户**过去某时刻独立发布**的——不要跟其它消息合并成同一个商品。
		// 详见 recognizeSystemPrompt 里"聊天记录展开"那节。
		if m.FromForwardChat {
			segs = append(segs, "[来自聊天记录]")
		}
		for _, seg := range m.Segments {
			switch seg.Type {
			case "text":
				if td, err := model.AsTextData(seg.Data); err == nil {
					segs = append(segs, td.Text)
				}
			case "image":
				segs = append(segs, "[图片]")
			case "video":
				// 视频段直接跳过——app 不展示视频、dispatch 也不会上传视频到 OSS。
				// 让 Kimi 完全感知不到时间线上有视频，避免它把 [视频] 占位符当作
				// "用户发了什么" 误判。详见 skill/bot/recognition.md "视频处理"。
				continue
			case "at":
				segs = append(segs, "[at]")
			case "face":
				segs = append(segs, "[表情]")
			case "reply":
				segs = append(segs, "[引用回复]")
			case "forward":
				// 走到这里说明 expandAndPushForward 失败兜底——子消息没拿到，
				// 给 Kimi 一个明确的占位符，避免误识别为别的业务动作。
				segs = append(segs, "[合并转发(未展开)]")
			default:
				segs = append(segs, "["+seg.Type+"]")
			}
		}
		// 优先用 OriginTime（来自聊天记录的节点）展示真实发送时间；零值 fallback Time
		displayTime := m.Time
		if !m.OriginTime.IsZero() {
			displayTime = m.OriginTime
		}
		msgs = append(msgs, kimi.RecognizeMsg{
			MessageID: m.MessageID,
			Time:      displayTime.Format("15:04:05"),
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

// buildAckMessage 把识别到的 RecognizeAction 翻成一条群里 @ 用户的占位回执。
//
// 仅在 global.Hfut == nil（HFUT_API_URL/TOKEN 未配齐）时被 processSnapshot 调到——
// 线上完整环境不会走这里。文案站在普通用户视角："服务暂时不可用" 比 "业务对接中"
// 更直白，不暴露专业 ID/技术字段。
func buildAckMessage(a kimi.RecognizeAction) string {
	switch a.Type {
	case "publish_good":
		category := "二手"
		if a.Category == 2 {
			category = "求物品"
		}
		return fmt.Sprintf("%s「%s」暂不可发布，稍后再试",
			category, orPlaceholder(a.Title, "未命名"))

	case "publish_question":
		return fmt.Sprintf("求解答「%s」暂不可发，稍后再试",
			orPlaceholder(a.QuestionTitle, "未命名"))

	case "publish_answer":
		hint := orPlaceholder(a.AnswerHintTo, "那条")
		return fmt.Sprintf("回答「%s」暂不可发，稍后再试", hint)

	case "off_shelf":
		hint := orPlaceholder(a.OffShelfHint, "商品")
		return fmt.Sprintf("下架「%s」暂不可用，稍后再试", hint)

	case "close_question":
		hint := orPlaceholder(a.CloseQuestionHint, "求解答")
		return fmt.Sprintf("关闭「%s」暂不可用，稍后再试", hint)

	case "seek_goods":
		return fmt.Sprintf("求物品「%s」暂不可发布，稍后再试",
			orPlaceholder(a.SeekHint, "物品"))

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

// batchItemCount 数批量上架 title 里逗号串联的商品个数。支持中文逗号 "，"、
// 半角逗号 ","、顿号 "、"，三种都常见。空 title 返回 0。
func batchItemCount(title string) int {
	t := strings.TrimSpace(title)
	if t == "" {
		return 0
	}
	// 把所有分隔符替换成 "," 再 split
	t = strings.ReplaceAll(t, "，", ",")
	t = strings.ReplaceAll(t, "、", ",")
	parts := strings.Split(t, ",")
	count := 0
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			count++
		}
	}
	return count
}

// normalizeBatchContent 把批量上架的 Kimi description 强制规范成"每件商品独占一行、
// 行尾中文句号"的格式——避免 Kimi 偶尔用顿号 / 中文逗号 / 段不换行等不一致输出。
//
// 拆分策略：按 "\n"、中文句号"。"、英文句号"."、顿号"、" 分段。每段 trim 后非空就
// 当成一件商品；行尾统一补"。"。行之间用 "\n" 连接。
func normalizeBatchContent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// 统一各种分隔符 → "\n"
	repl := strings.NewReplacer(
		"。\n", "\n",
		"。", "\n",
		"\r\n", "\n",
		"\r", "\n",
		"、", "\n",
	)
	t := repl.Replace(s)
	parts := strings.Split(t, "\n")
	lines := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// 去掉行尾可能残留的中英文逗号 / 句号 / 顿号
		p = strings.TrimRight(p, "，,、.。 ")
		if p == "" {
			continue
		}
		lines = append(lines, p+"。")
	}
	return strings.Join(lines, "\n")
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
