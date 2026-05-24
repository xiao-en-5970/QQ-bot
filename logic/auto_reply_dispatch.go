// Package logic 的 auto_reply_dispatch.go 把 RecognizeAction 翻译成对 hfut 后端的调用，
// 是 P0 占位 ack 升级到"真上架/下架/提问/回答"的 P1.4 实施。
//
// 高层流程：
//
//	processSnapshot 拿到识别结果 → 对每条 action 调 dispatchActionToHfut →
//	  upsert 旗下账号 → 按 action.Type 分流到 publish/off-shelf/article 等 →
//	  返回 ackResult{text, kind}：text 是要群里 @ 用户的文字，kind 是回执等级。
//	processSnapshot 按 conf.Group.AutoReplyVerbosity 决定要不要把这条回执真发到群里。
//
// 回执等级（ackKind）：
//
//	ackKindSuccess  真落库成功——任何模式都发；文案里**不**带 ID/技术字段，只展示
//	                用户能看懂的标题/价格/地点等
//	ackKindDup      去重命中（成功阻止重复，用户需要知道）——任何模式都发
//	ackKindAskUser  反问（多个在售/没指明哪条）——任何模式都发，用户主动发起的必须反馈
//	ackKindFail     hfut 同步失败/网络错——只在 verbose 模式发，normal 模式静默
//	ackKindIgnore   完全静默（如群没配学校）——任何模式都不发
//
// global.Hfut == nil 时（HFUT_API_URL/TOKEN 没配齐）这文件不会被调用，
// auto_reply.go 会回退到 P0 的 buildAckMessage 占位 ack。
package logic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"qq_bot/global"
	"qq_bot/model"
	"qq_bot/utils/client_pool"
	"qq_bot/utils/hfut"
	"qq_bot/utils/kimi"
	"qq_bot/utils/metrics"
	zaplog "qq_bot/utils/zap"
	"strconv"
	"strings"
	"time"
)

// ackKind 回执等级枚举——决定 processSnapshot 要不要把这条 ack 真发到群里。
//
// 详见包头注释。
type ackKind int

const (
	ackKindIgnore  ackKind = iota // 完全静默，任何模式都不发
	ackKindFail                   // 同步 hfut 失败 / 网络错——verbose 才发，normal 静默
	ackKindAskUser                // 反问，必须发
	ackKindDup                    // 去重命中，必须发
	ackKindSuccess                // 真落库成功，必须发
)

// ackResult dispatch 函数的统一返回类型——把"回执文字"和"回执等级"绑在一起，
// 让 processSnapshot 那一层根据 verbosity 决定是否发出。
type ackResult struct {
	Text string
	Kind ackKind
}

// shouldEmit 当前 verbosity 下这条 ack 是否应该真发到群里。
func (r ackResult) shouldEmit(verbose bool) bool {
	if r.Text == "" {
		return false
	}
	switch r.Kind {
	case ackKindIgnore:
		return false
	case ackKindFail:
		return verbose // normal 模式静默
	default: // success / dup / ask_user
		return true
	}
}

// dispatchActionToHfut 把一条 RecognizeAction 落到 hfut 后端，返回 ackResult。
//
// processSnapshot 那一层按 conf.Group.AutoReplyVerbosity 决定 ackResult 是否真发到群。
//
// 输入 snap 用于按 ImageMessageIDs 还原图片 URL 给 publish_good。
//
// skipRateLimit=true 表示调用方已经在 snapshot 入口处统一取过限流令牌，本次直接
// 跳过 per-action 计数——避免"一个快照识别出 10 个商品时被算 10 次"。
func dispatchActionToHfut(
	ctx context.Context,
	key autoReplyBucketKey,
	userCard string,
	snap []autoReplyMsg,
	action kimi.RecognizeAction,
	skipRateLimit bool,
) ackResult {
	// 第 1 步：限流（P3.4）——仅对会"落库 / 改状态"的动作生效。
	// 反问类（off_shelf 多候选 / close_question 多候选）落到这里其实只是"反问 + 等回应"，
	// 也算一次 dispatch，但这一类不计数（避免用户被反问后立刻又触发限流）。
	//
	// 注意：skipRateLimit=true 时调用方（processSnapshot / processImageOnlySnapshot）
	// 已经按"快照"取过一次令牌，整批 action 共用，这里不再重复计数。
	if !skipRateLimit && isMutatingAction(action.Type) {
		if ok, retry := dispatchLimiter.Allow(key); !ok {
			metrics.IncRateLimit()
			zaplog.Logger.Warnf("autoReply 限流命中 group=%d user=%d type=%s retry=%s",
				key.GroupID, key.UserID, action.Type, retry)
			return ackResult{
				Text: fmt.Sprintf("太快，%d 秒后再发", int(retry.Seconds())),
				Kind: ackKindAskUser,
			}
		}
	}

	// 第 2 步：upsert 旗下账号——所有写操作都需要 user_id。
	//
	// 同时把 QQ 群名片（userCard）和拼出来的头像 CDN URL 上报给 hfut——
	// hfut 用"最新覆盖"策略写到 users.nickname / users.qq_avatar_url，
	// 个人展示页与作者卡片优先用这两个字段（详见 hfut model.User.DisplayName / DisplayAvatarPath）。
	upsert, err := global.Hfut.UpsertQQChild(ctx, qqNumberOf(key.UserID), key.GroupID,
		userCard, qqAvatarURL(key.UserID))
	if err != nil {
		// 群没配学校 → 完全静默（按 SKILL.md 设计）
		if errors.Is(err, hfut.ErrGroupNoSchool) {
			zaplog.Logger.Debugf("autoReply group=%d user=%d 群没配学校，静默忽略 action %s",
				key.GroupID, key.UserID, action.Type)
			return ackResult{Kind: ackKindIgnore}
		}
		zaplog.Logger.Errorf("autoReply hfut UpsertQQChild 失败 group=%d user=%d: %v", key.GroupID, key.UserID, err)
		return ackResult{Text: "忙，稍后再试", Kind: ackKindFail}
	}
	if upsert.Created {
		zaplog.Logger.Infof("autoReply 创建旗下账号 group=%d qq=%d → user_id=%d school_id=%d",
			key.GroupID, key.UserID, upsert.UserID, upsert.SchoolID)
	}

	// 第 3 步：按 action.Type 分流到具体 hfut 调用。
	switch action.Type {
	case "publish_good":
		return dispatchPublishGood(ctx, key, upsert.UserID, snap, action)
	case "publish_question":
		return dispatchPublishQuestion(ctx, key, upsert.UserID, snap, action)
	case "publish_answer":
		return dispatchPublishAnswer(ctx, key.GroupID, upsert.UserID, action)
	case "off_shelf":
		return dispatchOffShelf(ctx, key, upsert.UserID, snap, action)
	case "close_question":
		return dispatchCloseQuestion(ctx, key, upsert.UserID, action)
	case "seek_goods":
		return dispatchSeekGoods(ctx, key, upsert.UserID, snap, action)
	default:
		// 未知 type 不该走到这里（processSnapshot 那边已经过滤过 none）
		return ackResult{Kind: ackKindIgnore}
	}
}

// isMutatingAction 判定一个 action 是否会"真改 hfut 状态"——只有这些才计数限流。
//
// seek_goods 也算 mutating：它会把"求购"作为 publish_good(category=2, 面议) 真正落库。
func isMutatingAction(actionType string) bool {
	switch actionType {
	case "publish_good", "publish_question", "publish_answer", "seek_goods":
		return true
	}
	return false
}

// qqNumberOf 把 NapCat 的 user_id（int64）转成 qq_number 字符串，
// 跟 hfut 那边 user.qq_number 字段对齐（varchar）。
func qqNumberOf(userID int64) string {
	return fmt.Sprintf("%d", userID)
}

// qqAvatarURL 拼出指定 QQ 号的头像 CDN URL。
//
// 用 q.qlogo.cn/headimg_dl 接口——它是 QQ 官方提供的公开头像服务，URL 永久有效，
// 头像更新后下次拉取自动是新图（无需任何 token / API key）。spec 控制尺寸，
// 640 px 足够前端 retina 显示头像 + 用户列表头像（同一份）。
//
// 接口文档：https://q.qlogo.cn/headimg_dl?dst_uin={qq}&spec=640
//
// userID == 0 / 负数 → 返空串。
func qqAvatarURL(userID int64) string {
	if userID <= 0 {
		return ""
	}
	return fmt.Sprintf("https://q.qlogo.cn/headimg_dl?dst_uin=%d&spec=640", userID)
}

// dispatchSeekGoods 处理「收/求/求购/收购 + 物品」（无价场景）：
//
//  1. 在本校在售二手里搜一下，命中则**异步**给求购者发"@用户 你可能在找：xxx"普通提示
//     + 一个合并转发"聊天记录卡片"（商品文字 / 配图 / 联系方式），让用户能直接看到
//     卖家信息。卡片不依赖 QQ 历史消息（OSS 图片永久有效；卖家是孤儿 QQ 号时附 QQ
//     联系方式，非孤儿则提示在 app 内联系）。
//  2. 不管是否命中，都把求购意图作为 publish_good(category=2 求物品, price=0,
//     negotiable=false) 落库——让 app 用户也能看到。hfut 自动 7 天后下架（详见
//     service.BotPublishGood 的 ttl 逻辑）。
//
// 两步异步并行：合并转发卡片不阻塞 PublishGood 调用，PublishGood 失败也不影响卡片
// 发送。
func dispatchSeekGoods(ctx context.Context, key autoReplyBucketKey, userID uint, snap []autoReplyMsg, a kimi.RecognizeAction) ackResult {
	title := strings.TrimSpace(a.SeekHint)
	if title == "" {
		return ackResult{Kind: ackKindIgnore}
	}

	// 第 1 步：本校在售里检索匹配商品（最多 top 3）；命中即异步发卡片（不阻塞主流程）
	if matches := seekTopMatches(ctx, key.GroupID, title, seekForwardMaxItems); len(matches) > 0 {
		// goroutine 里发送，主流程继续；卡片自带 silentSuppressGroup 门
		go sendSeekMatchForwardCard(key.GroupID, key.UserID, title, matches)
	}

	// 第 2 步：上架为「求物品」（cat=2、price=0、非面议——前端隐藏价格、不挂"有偿"tag）
	desc := strings.TrimSpace(a.Description)
	if desc == "" {
		desc = firstTextFromSnap(snap)
	}
	if desc == "" {
		desc = title
	}

	// 收集本次"上架求物品"涉及的 QQ message_id；让用户 reply 自己之前的求购消息说"已求到"时
	// 能精确反查到 good。
	botMsgIDs := collectBotMessageIDs(snap, a)

	seekReq := hfut.PublishGoodReq{
		UserID:        userID,
		GroupID:       key.GroupID,
		Title:         title,
		Content:       desc,
		Category:      2,
		Negotiable:    false,
		Price:         0,
		BotMessageIDs: botMsgIDs,
	}
	pubResp, pubErr := global.Hfut.PublishGood(ctx, seekReq)

	if pubErr != nil {
		var dup *hfut.DuplicateGoodInfo
		if errors.As(pubErr, &dup) {
			zaplog.Logger.Infof("autoReply seek_goods 去重命中 user=%d title=%q existing=%d/%q",
				userID, title, dup.ExistingID, dup.ExistingTitle)
			origReq := seekReq // 拷贝快照供反问 followup 重发起 publish
			dupOffShelfMgr.Save(key, userID, dup.ExistingID, dup.ExistingTitle, &origReq)
			dupTitle := dup.ExistingTitle
			if dupTitle == "" {
				dupTitle = title
			}
			return ackResult{
				Text: fmt.Sprintf("求物品「%s」已发过（id=%d）。回复 1=重复上架 / 2=下架旧的并上架",
					dupTitle, dup.ExistingID),
				Kind: ackKindDup,
			}
		}
		zaplog.Logger.Errorf("autoReply seek_goods PublishGood 失败 user=%d title=%q: %v", userID, title, pubErr)
		return ackResult{
			Text: fmt.Sprintf("求物品「%s」未发出，稍后再试", title),
			Kind: ackKindFail,
		}
	}

	// 记录"最近一条"——求物品也按 cat=2 落到 recentGoodMgr，后续"不要了"可定位
	if pubResp != nil {
		recentGoodMgr.Save(key, userID, pubResp.GoodID, title, 2)
	}

	// 上架成功后异步上报运维群（不阻塞主回执）
	go NotifyOpsPublish(nil, key.GroupID, key.UserID, "", "求物品(无价/求购)", title,
		fmt.Sprintf("发起人 user_id: %d", userID))

	return ackResult{
		Text: fmt.Sprintf("已发布 求「%s」", title),
		Kind: ackKindSuccess,
	}
}

// seekForwardMaxItems 合并转发卡片里最多列几个匹配项（商品 / 求购者都按此上限）。
// 太多会让聊天记录卡片很长，群友点开后疲劳；3 个能覆盖绝大多数有用匹配。
const seekForwardMaxItems = 3

// seekTopMatches 拉 SearchGoodsSeek top N 命中；不命中或失败返回 nil。
// 失败（含群没配学校）也返回 nil 让上游平滑降级；warn 不打日志干扰主路径。
func seekTopMatches(ctx context.Context, groupID int64, q string, n int) []hfut.SeekGoodMatch {
	if n <= 0 {
		n = seekForwardMaxItems
	}
	list, err := global.Hfut.SearchGoodsSeek(ctx, groupID, q, n)
	if err != nil {
		if !errors.Is(err, hfut.ErrGroupNoSchool) {
			zaplog.Logger.Warnf("autoReply seek 检索失败 group=%d q=%q: %v", groupID, q, err)
		}
		return nil
	}
	if len(list) > n {
		list = list[:n]
	}
	return list
}

// seekerTopMatches 同上，但反查的是"在求 X 的求购者"（goods_category=2 求物品）。
// 用户上架商品后，bot 用本函数拿到"群里求该商品的最近 N 位"列表，发合并转发卡片
// 提示卖家"以下人可能需要"。
func seekerTopMatches(ctx context.Context, groupID int64, q string, n int) []hfut.SeekerMatch {
	if n <= 0 {
		n = seekForwardMaxItems
	}
	list, err := global.Hfut.SearchActiveSeeks(ctx, groupID, q, n)
	if err != nil {
		if !errors.Is(err, hfut.ErrGroupNoSchool) {
			zaplog.Logger.Warnf("autoReply seeker 反查失败 group=%d q=%q: %v", groupID, q, err)
		}
		return nil
	}
	if len(list) > n {
		list = list[:n]
	}
	return list
}

// sendSeekMatchForwardCard 把"匹配到的若干在售商品"打包成合并转发卡片发到求购群。
//
// 流程：
//
//  1. 先发普通 `@求购用户 你可能在找：xx商品名（找到 N 个匹配商品）` 让用户立刻
//     看到 @ 提示，而不必点开聊天记录卡片才知道是给自己的
//  2. 再发合并转发包：每个商品 3 段节点（文字 + 图 + 联系方式），最多 3 个商品
//
// SilentMode 静默：两次发送都走 silentSuppressGroup 门，与其它群消息出口一致。
// 失败仅 warn 不重试——seek 命中只是锦上添花提示。
func sendSeekMatchForwardCard(groupID, seekerQQ int64, seekTitle string, matches []hfut.SeekGoodMatch) {
	if len(matches) == 0 {
		return
	}
	httpClient := client_pool.NewClientPool()

	heading := fmt.Sprintf("你可能在找：%s", strings.TrimSpace(seekTitle))
	if len(matches) > 1 {
		heading += fmt.Sprintf("（找到 %d 个匹配商品）", len(matches))
	}
	if err := SendGroupAtText(httpClient, groupID, seekerQQ, heading); err != nil {
		zaplog.Logger.Warnf("autoReply seek 命中提示发送失败 group=%d qq=%d: %v",
			groupID, seekerQQ, err)
	}

	nodes := buildSeekForwardNodes(matches)
	if len(nodes) == 0 {
		return
	}
	if err := SendGroupForwardMsg(httpClient, groupID, nodes); err != nil {
		zaplog.Logger.Warnf("autoReply seek 合并转发卡片发送失败 group=%d items=%d: %v",
			groupID, len(matches), err)
	}
}

// buildSeekForwardNodes 构造"匹配商品列表"合并转发卡片的节点序列。
//
// 每商品 3 段节点：[文字 商品 i 信息] → [图 1..K] → [文字 联系方式 i]
// 每商品最多 3 张图（节点总数封顶在 ~15，群友能从容滑过去）。
func buildSeekForwardNodes(matches []hfut.SeekGoodMatch) []model.ForwardNodeIn {
	const maxImagesPerItem = 3
	if len(matches) == 0 {
		return nil
	}
	nodes := make([]model.ForwardNodeIn, 0, len(matches)*5)

	for i, g := range matches {
		// 商品文字节点
		var b strings.Builder
		if len(matches) > 1 {
			fmt.Fprintf(&b, "[%d] ", i+1)
		}
		fmt.Fprintf(&b, "商品：%s", strings.TrimSpace(g.Title))
		switch {
		case g.Negotiable:
			b.WriteString("\n价格：面议")
		case g.Price > 0:
			fmt.Fprintf(&b, "\n价格：%g 元", float64(g.Price)/100)
		default:
			b.WriteString("\n价格:免费")
		}
		if loc := strings.TrimSpace(g.Location); loc != "" {
			fmt.Fprintf(&b, "\n地点：%s", loc)
		}
		if created, perr := parseHFUTTime(g.CreatedAt); perr == nil {
			days := int(time.Since(created).Hours() / 24)
			if days < 0 {
				days = 0
			}
			fmt.Fprintf(&b, "\n上架：约 %d 天前", days)
		}
		if content := strings.TrimSpace(g.Content); content != "" {
			fmt.Fprintf(&b, "\n描述：%s", truncateForLog(content, 200))
		}
		nodes = append(nodes, BuildForwardNodeText(b.String()))

		// 商品图节点（每商品最多 3 张）
		shown := 0
		for _, url := range g.Images {
			if shown >= maxImagesPerItem {
				break
			}
			url = strings.TrimSpace(url)
			if url == "" {
				continue
			}
			nodes = append(nodes, BuildForwardNodeImage(url))
			shown++
		}

		// 联系方式节点：hfut 端 botContactQQForUser 已经按"孤儿 / 主账号已绑 QQ"
		// 两种情况都填充了 SellerQQ；只有完全没绑 QQ 的纯 app 主账号才会空，
		// 这时才 fallback 到 app 内搜索。
		var contact string
		if qq := strings.TrimSpace(g.SellerQQ); qq != "" {
			contact = fmt.Sprintf("联系卖家：QQ %s", qq)
		} else {
			contact = fmt.Sprintf("联系卖家：请在 app 内搜索「%s」查看完整信息和私聊", strings.TrimSpace(g.Title))
		}
		nodes = append(nodes, BuildForwardNodeText(contact))
	}

	return nodes
}

// sendSellerMatchForwardCard 把"匹配到的若干求购者"打包成合并转发卡片发到上架群。
//
// 跟 sendSeekMatchForwardCard 对称——卖家在群里"出 X" 上架成功后，bot 反查谁在
// 求 X，命中即提示卖家"以下人可能需要"。
func sendSellerMatchForwardCard(groupID, sellerQQ int64, sellTitle string, matches []hfut.SeekerMatch) {
	if len(matches) == 0 {
		return
	}
	httpClient := client_pool.NewClientPool()

	heading := fmt.Sprintf("以下人可能需要：%s", strings.TrimSpace(sellTitle))
	if len(matches) > 1 {
		heading += fmt.Sprintf("（找到 %d 个匹配求购者）", len(matches))
	}
	if err := SendGroupAtText(httpClient, groupID, sellerQQ, heading); err != nil {
		zaplog.Logger.Warnf("autoReply seller 命中提示发送失败 group=%d qq=%d: %v",
			groupID, sellerQQ, err)
	}

	nodes := buildSellerMatchForwardNodes(matches)
	if len(nodes) == 0 {
		return
	}
	if err := SendGroupForwardMsg(httpClient, groupID, nodes); err != nil {
		zaplog.Logger.Warnf("autoReply seller 合并转发卡片发送失败 group=%d items=%d: %v",
			groupID, len(matches), err)
	}
}

// buildSellerMatchForwardNodes 构造"匹配求购者列表"合并转发卡片的节点序列。
//
// 每个求购者一个**单文字节点**（求物品场景没有图、没有地点，比商品列表简单）。
func buildSellerMatchForwardNodes(matches []hfut.SeekerMatch) []model.ForwardNodeIn {
	if len(matches) == 0 {
		return nil
	}
	nodes := make([]model.ForwardNodeIn, 0, len(matches))
	for i, s := range matches {
		var b strings.Builder
		if len(matches) > 1 {
			fmt.Fprintf(&b, "[%d] ", i+1)
		}
		fmt.Fprintf(&b, "求购：%s", strings.TrimSpace(s.Title))
		switch {
		case s.Negotiable:
			b.WriteString("\n愿付：面议")
		case s.Price > 0:
			fmt.Fprintf(&b, "\n愿付：%g 元", float64(s.Price)/100)
		default:
			b.WriteString("\n愿付：仅求购无酬劳")
		}
		if created, perr := parseHFUTTime(s.CreatedAt); perr == nil {
			days := int(time.Since(created).Hours() / 24)
			if days < 0 {
				days = 0
			}
			fmt.Fprintf(&b, "\n求购时间：约 %d 天前", days)
		}
		if content := strings.TrimSpace(s.Content); content != "" {
			fmt.Fprintf(&b, "\n描述：%s", truncateForLog(content, 200))
		}
		// 联系方式：同卖家匹配逻辑，hfut 已按 botContactQQForUser 填好 SeekerQQ；
		// 任何能拿到 QQ 的求购者（孤儿 / 已绑 QQ 的主账号）都给 QQ 号。
		if qq := strings.TrimSpace(s.SeekerQQ); qq != "" {
			fmt.Fprintf(&b, "\n联系求购者：QQ %s", qq)
		} else {
			fmt.Fprintf(&b, "\n联系求购者：请在 app 内搜索「%s」查看完整信息和私聊",
				strings.TrimSpace(s.Title))
		}
		nodes = append(nodes, BuildForwardNodeText(b.String()))
	}
	return nodes
}

// firstTextFromSnap 从窗口里挑第一条非空文本，作为 publish_good 的 content 兜底。
func firstTextFromSnap(snap []autoReplyMsg) string {
	for _, m := range snap {
		if t := strings.TrimSpace(m.FlatText); t != "" {
			return t
		}
	}
	return ""
}

// joinAckLines 用换行串多行 ack；空行自动跳过。
func joinAckLines(lines ...string) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, "\n")
}

func parseHFUTTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty")
	}
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if t, e := time.ParseInLocation(layout, s, time.Local); e == nil {
			return t, nil
		}
		if t, e := time.Parse(layout, s); e == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unknown time layout")
}

// =============================================================================
// publish_good — 上架商品
// =============================================================================

// dispatchPublishGood 把 publish_good action 落库。key.GroupID 是 bot 收到该消息的 QQ 群号——
// 后端会持久化到 goods.created_in_group_id，给孤儿商品 "请求下架" 提供精准的 @ 卖家位置。
func dispatchPublishGood(ctx context.Context, key autoReplyBucketKey, userID uint, snap []autoReplyMsg, a kimi.RecognizeAction) ackResult {
	groupID := key.GroupID
	// 用 ImageMessageIDs 还原图片 URL（NapCat 临时 URL）
	napcatImages := imageURLsFromSnap(snap, a.ImageMessageIDs)
	// 转存到 hfut OSS 拿永久 URL；任一张转存失败就 skip 那张（不让整体上架失败）
	images := mirrorImagesToHfut(ctx, userID, napcatImages)

	// 价格 / 面议判定（产品形态：cat=1 二手；cat=2 求物品）
	//
	//   - 模型 negotiable=true → 用户**显式**写了"面议"，前端展示"面议"
	//   - 模型给了 price → 走价格分；priceCents = price*100
	//   - 完全没说价：cat=1 默认面议（卖东西需要让买家私聊）；
	//                  cat=2 保持 price=0 + negotiable=false（前端不展示价格、也不挂"有偿"tag）
	priceCents := 0
	if a.Price != nil {
		priceCents = int(*a.Price * 100) // 元 → 分
		if priceCents < 0 {
			priceCents = 0
		}
	}
	negotiable := a.Negotiable
	if !negotiable && a.Price == nil && a.Category == 1 {
		negotiable = true
	}

	// 收集本次上架涉及的全部 QQ message_id：snap 里每条外层消息的 MessageID + 模型
	// 给出的 ImageMessageIDs / SourceMessageIDs；落到 hfut goods.bot_message_ids 后，
	// 用户 reply 任一条都能反查到 good 直接下架（详见 LookupActiveGoodByMessageID）。
	botMsgIDs := collectBotMessageIDs(snap, a)

	// 批量上架：content 强制按"每件一行、行尾句号、行间换行"标准化；普通上架走原样
	content := strings.TrimSpace(a.Description)
	if a.IsBatch {
		content = normalizeBatchContent(content)
	}
	// resp 里有 GoodID 用于"最近一条"快速查找；不在群里展示给用户——
	// 用户在 app "我的发布" 列表能看到刚发的，没必要再给个数字增加阅读负担。
	pubReq := hfut.PublishGoodReq{
		UserID:        userID,
		GroupID:       groupID,
		Title:         strings.TrimSpace(a.Title),
		Content:       content,
		Category:      int16(a.Category),
		Negotiable:    negotiable,
		Bargain:       a.Bargain,
		Price:         priceCents,
		Stock:         a.Stock, // <=0 时 hfut 后端按 1 兜底（详见 BotPublishGood 注释）
		Location:      strings.TrimSpace(a.Location),
		Images:        images,
		BotMessageIDs: botMsgIDs,
		IsBatch:       a.IsBatch, // 合并聊天记录的"批量上架"商品；hfut 落 goods.is_batch
	}
	resp, err := global.Hfut.PublishGood(ctx, pubReq)
	if err != nil {
		// 去重保护命中——不视为失败，给用户友好提示，不重复上架
		var dup *hfut.DuplicateGoodInfo
		if errors.As(err, &dup) {
			zaplog.Logger.Infof("autoReply 去重命中 user=%d title=%q existing=%d/%q",
				userID, a.Title, dup.ExistingID, dup.ExistingTitle)
			origReq := pubReq // 拷贝供反问 followup 用
			dupOffShelfMgr.Save(key, userID, dup.ExistingID, dup.ExistingTitle, &origReq)
			showTitle := dup.ExistingTitle
			if showTitle == "" {
				showTitle = strings.TrimSpace(a.Title)
			}
			if showTitle == "" {
				showTitle = "这条"
			}
			return ackResult{
				Text: fmt.Sprintf("「%s」已在售（id=%d）。回复 1=重复上架 / 2=下架旧的并上架",
					showTitle, dup.ExistingID),
				Kind: ackKindDup,
			}
		}
		zaplog.Logger.Errorf("autoReply hfut PublishGood 失败 user=%d title=%q: %v", userID, a.Title, err)
		return ackResult{
			Text: fmt.Sprintf("「%s」未发出，稍后再试", orPlaceholder(a.Title, "这条")),
			Kind: ackKindFail,
		}
	}

	// 成功回执——只保留**用户最关心的核心字段**：分类 + 标题 + 价格 + 可选数量。
	//
	// 设计原则：群里的 ack 越精简越好。地点 / 配图数 / 描述这些信息在 app 详情页
	// 都有，群消息里堆这些反而像"bot 在告诉群友我能搞清楚什么"。统一文案形态：
	//
	//   - 二手有价：     已发布 二手「鞋架」 6 元
	//   - 二手面议：     已发布 二手「鞋架」 面议
	//   - 求物品有偿：   已发布 求「电瓶车」 50 元（有偿）
	//   - 求物品无价：   已发布 求「电瓶车」
	//   - 带数量：       已发布 二手「鞋架」 6 元 × 3
	//
	// 注意：QQ 上架的商品 / 求物品有自动有效期（二手 30 天、求物品 7 天，由 hfut
	// service.BotPublishGood 自动设 deadline），但**不在群回执里提**——app 端
	// deadline 标签会显示剩余时间，群里少一行减少干扰。
	category := "二手"
	if a.Category == 2 {
		category = "求"
	}
	var b strings.Builder
	if a.IsBatch {
		// 批量上架：直接 "批量上架 N 件商品：a，b，c。"——不带"已发布"前缀、
		// 不带"二手"/"求"分类、不带价格（统一面议）。商品名列表直接用 title
		// 原始内容（Kimi 已经按中文逗号串联）。
		itemCount := batchItemCount(a.Title)
		title := strings.TrimSpace(a.Title)
		if itemCount > 1 {
			fmt.Fprintf(&b, "批量上架 %d 件商品：%s。", itemCount, title)
		} else {
			fmt.Fprintf(&b, "批量上架商品：%s。", orPlaceholder(title, "未命名"))
		}
	} else {
		b.WriteString("已发布 ")
		b.WriteString(category)
		b.WriteString("「")
		b.WriteString(orPlaceholder(a.Title, "未命名"))
		b.WriteString("」")
		switch {
		case negotiable:
			b.WriteString(" 面议")
		case priceCents > 0:
			fmt.Fprintf(&b, " %g 元", float64(priceCents)/100)
			if a.Category == 2 {
				b.WriteString("（有偿）")
			}
		}
		if a.Stock > 1 {
			fmt.Fprintf(&b, " × %d", a.Stock)
		}
	}
	// 记录"最近一条"——给后续"不要了 / 不卖了"上下文化处理用
	if resp != nil {
		recentGoodMgr.Save(key, userID, resp.GoodID, strings.TrimSpace(a.Title), a.Category)
	}

	// 上架成功后异步反查"谁在求 X" —— 仅二手卖出（cat=1）场景有意义；求物品 (cat=2)
	// 反查求物品本身没意义（用户 A 求 X, B 也求 X, 互相不能满足对方）。
	if a.Category == 1 {
		sellTitle := strings.TrimSpace(a.Title)
		if sellTitle != "" {
			go func() {
				// 独立 ctx，不被本次 dispatch ctx 影响（dispatchActionToHfut 的 ctx
				// 一般是 processSnapshot 那层的 60s，反查 + 发卡片可能超出）
				bgCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				matches := seekerTopMatches(bgCtx, key.GroupID, sellTitle, seekForwardMaxItems)
				if len(matches) == 0 {
					return
				}
				sendSellerMatchForwardCard(key.GroupID, key.UserID, sellTitle, matches)
			}()
		}
	}

	// 上架成功后异步上报运维群（不阻塞主回执）
	opsKind := "二手"
	if a.Category == 2 {
		opsKind = "求物品"
	}
	opsPriceLine := ""
	switch {
	case negotiable:
		opsPriceLine = "价格: 面议"
	case priceCents > 0:
		if a.Category == 2 {
			opsPriceLine = fmt.Sprintf("价格: %g 元（有偿）", float64(priceCents)/100)
		} else {
			opsPriceLine = fmt.Sprintf("价格: %g 元", float64(priceCents)/100)
		}
	default:
		opsPriceLine = "价格: 0 / 无"
	}
	opsLoc := ""
	if a.Location != "" {
		opsLoc = "地点: " + a.Location
	}
	opsImgs := ""
	if len(images) > 0 {
		opsImgs = fmt.Sprintf("配图: %d 张", len(images))
	}
	go NotifyOpsPublish(nil, key.GroupID, key.UserID, "", opsKind, strings.TrimSpace(a.Title),
		opsPriceLine, opsLoc, opsImgs, fmt.Sprintf("user_id: %d", userID))

	return ackResult{
		Text: b.String(),
		Kind: ackKindSuccess,
	}
}

// imageURLsFromSnap 按 message_id 在 snap 里找回原 segments，提取图片 URL（NapCat 临时 URL）。
//
// 这一步只是"把窗口里的图找出来"——返回的还是 NapCat 临时 URL，没有持久化。
// 调用方拿到这个列表后应该再走 mirrorImagesToHfut 把 URL 转成 hfut OSS 的永久 URL，
// 再传给 PublishGood / PublishArticle 入库——否则 NapCat URL 几天后过期商品图就死链。
func imageURLsFromSnap(snap []autoReplyMsg, msgIDs []int64) []string {
	if len(msgIDs) == 0 {
		return nil
	}
	idSet := make(map[int64]struct{}, len(msgIDs))
	for _, id := range msgIDs {
		idSet[id] = struct{}{}
	}
	var urls []string
	for _, m := range snap {
		if _, ok := idSet[m.MessageID]; !ok {
			continue
		}
		for _, seg := range m.Segments {
			if seg.Type != "image" {
				continue
			}
			img, err := model.AsImageData(seg.Data)
			if err != nil {
				continue
			}
			// NapCat 在 image segment 的 data 里有 url / file 两个字段，url 是公网可达的；
			// 偶尔只有 file（base64 / 本地路径），那种 case 这里跳过，避免给 hfut 存死链
			if img.URL != "" {
				urls = append(urls, img.URL)
			}
		}
	}
	return urls
}

// mirrorImageDownloadTimeout 单张图从 NapCat 拉回 bot 的最长时间。
//
// NapCat 的图床通常在腾讯 multimedia.nt.qq.com.cn，国内访问几百毫秒内回；
// 给到 15 秒已经留了网络抖动的余量。超时 = skip 这张图，不阻塞商品发布。
const mirrorImageDownloadTimeout = 15 * time.Second

// mirrorImageMaxBytes 单张图允许的最大字节数；跟 hfut 那边 BotUploadImageMaxBytes 对齐。
//
// 防御场景：恶意构造的 url 返回超大文件吃光 bot 内存。
// 真实群聊照片普遍 < 2MB，10MB 已经是非常富余的兜底。
const mirrorImageMaxBytes = 10 * 1024 * 1024

// mirrorImagesToHfut 把 NapCat 临时 URL 列表转成 hfut OSS 永久 URL 列表。
//
// 流程（每张图独立走一遍）：
//  1. http GET NapCat URL（带超时 + 大小上限）→ 拿到二进制
//  2. 从 URL path / Content-Type 推断扩展名（jpg/png/...）
//  3. 调 hfut.UploadImage 上传 → 拿到永久 URL
//
// 失败处理：**任何一张图的任何一步出错，仅 log + skip 这张**，继续下一张——
// 商品少一张图比让"商品创建失败"友好得多。最终返回成功上传的 URL 列表。
//
// 若 global.Hfut 没配（理论上 dispatch 不会走到这里，防御性兜底），原样返 napcatURLs。
func mirrorImagesToHfut(ctx context.Context, userID uint, napcatURLs []string) []string {
	if len(napcatURLs) == 0 {
		return nil
	}
	if global.Hfut == nil {
		zaplog.Logger.Warnf("mirrorImagesToHfut: global.Hfut nil，回退到原 NapCat URL")
		return napcatURLs
	}
	out := make([]string, 0, len(napcatURLs))
	for i, srcURL := range napcatURLs {
		hfutURL, err := mirrorOneImage(ctx, userID, srcURL)
		if err != nil {
			zaplog.Logger.Warnf("mirrorImagesToHfut 第 %d/%d 张转存失败 user=%d url=%q: %v",
				i+1, len(napcatURLs), userID, truncateForLog(srcURL, 100), err)
			continue
		}
		out = append(out, hfutURL)
	}
	if len(out) < len(napcatURLs) {
		zaplog.Logger.Infof("mirrorImagesToHfut 部分失败 user=%d: %d/%d 成功",
			userID, len(out), len(napcatURLs))
	}
	return out
}

// mirrorOneImage 单张图的转存：下载 → 推断扩展名 → 上传。
//
// 单独抽出来是为了让 mirrorImagesToHfut 的循环逻辑清晰；并且每张图用独立 ctx
// 控制超时，一张失败不影响其它。
func mirrorOneImage(parent context.Context, userID uint, srcURL string) (string, error) {
	dlCtx, cancel := context.WithTimeout(parent, mirrorImageDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, srcURL, nil)
	if err != nil {
		return "", fmt.Errorf("构造下载请求: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载图片: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("下载图片 HTTP %d", resp.StatusCode)
	}

	// io.LimitReader 提前截断防 OOM；超过上限时拒收
	limited := io.LimitReader(resp.Body, mirrorImageMaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("读响应: %w", err)
	}
	if len(data) > mirrorImageMaxBytes {
		return "", fmt.Errorf("图片超过 %dMB 上限", mirrorImageMaxBytes/1024/1024)
	}
	if len(data) == 0 {
		return "", errors.New("下载到的图片为空")
	}

	filename := guessImageFilename(srcURL, resp.Header.Get("Content-Type"))

	// 上传给独立留 30s 超时——内网调用，足够。
	upCtx, upCancel := context.WithTimeout(parent, 30*time.Second)
	defer upCancel()
	resp2, err := global.Hfut.UploadImage(upCtx, userID, data, filename)
	if err != nil {
		return "", fmt.Errorf("上传到 hfut: %w", err)
	}
	return resp2.URL, nil
}

// guessImageFilename 给 hfut 那边一个像样的 filename；hfut 端只看扩展名做白名单。
//
// 优先级：URL path 里的扩展名 > Content-Type 推断 > 兜底 "img.jpg"。
// 主要是为了应对 NapCat URL 形如 .../xxx?token=... 这种带 query 的格式，
// 或者 URL path 完全没扩展名的极端情况。
func guessImageFilename(srcURL, contentType string) string {
	// 先看 URL path 末尾的扩展名（去掉 query / fragment）
	clean := srcURL
	if i := strings.IndexAny(clean, "?#"); i >= 0 {
		clean = clean[:i]
	}
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(strings.ToLower(clean), ext) {
			return "img" + ext
		}
	}
	// 再 fallback 到 Content-Type
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])) {
	case "image/jpeg", "image/jpg":
		return "img.jpg"
	case "image/png":
		return "img.png"
	case "image/gif":
		return "img.gif"
	case "image/webp":
		return "img.webp"
	}
	// 最后兜底
	return "img.jpg"
}

// =============================================================================
// publish_question / publish_answer / close_question — 提问 + 回答
// =============================================================================

func dispatchPublishQuestion(ctx context.Context, key autoReplyBucketKey, userID uint, snap []autoReplyMsg, a kimi.RecognizeAction) ackResult {
	napcatImages := imageURLsFromSnap(snap, a.ImageMessageIDs)
	images := mirrorImagesToHfut(ctx, userID, napcatImages)
	// resp.ArticleID 不外露——用户在 app "我的提问" 里就能看到刚发的，没必要给个数字。
	_, err := global.Hfut.PublishArticle(ctx, hfut.PublishArticleReq{
		UserID:  userID,
		Type:    2, // 提问
		Title:   strings.TrimSpace(a.QuestionTitle),
		Content: strings.TrimSpace(orPlaceholder(a.QuestionContent, a.QuestionTitle)), // content 兜底用 title
		Images:  images,
	})
	if err != nil {
		zaplog.Logger.Errorf("autoReply hfut PublishArticle(question) 失败 user=%d: %v", userID, err)
		return ackResult{
			Text: fmt.Sprintf("求解答「%s」未发出，稍后再试",
				orPlaceholder(a.QuestionTitle, "这条")),
			Kind: ackKindFail,
		}
	}
	// 上架成功后异步上报运维群
	go NotifyOpsPublish(nil, key.GroupID, key.UserID, "", "求解答", strings.TrimSpace(a.QuestionTitle),
		fmt.Sprintf("user_id: %d", userID))

	return ackResult{
		Text: fmt.Sprintf("已发布 问「%s」",
			orPlaceholder(a.QuestionTitle, "未命名")),
		Kind: ackKindSuccess,
	}
}

func dispatchPublishAnswer(ctx context.Context, groupID int64, userID uint, a kimi.RecognizeAction) ackResult {
	hint := strings.TrimSpace(a.AnswerHintTo)
	if hint == "" {
		return ackResult{
			Text: "答哪条？带上求解答里的关键词",
			Kind: ackKindAskUser,
		}
	}
	openQs, err := global.Hfut.ListOpenQuestions(ctx, groupID, 20)
	if err != nil {
		zaplog.Logger.Errorf("autoReply ListOpenQuestions 失败 group=%d: %v", groupID, err)
		return ackResult{Text: "回答未发出，稍后再试", Kind: ackKindFail}
	}
	parent := matchQuestion(openQs, hint)
	if parent == nil {
		return ackResult{
			Text: fmt.Sprintf("没找到「%s」相关求解答", hint),
			Kind: ackKindAskUser,
		}
	}
	pid := int(parent.ID)
	// resp.ArticleID 不外露——回答在 app 提问详情页就能看到。
	_, err = global.Hfut.PublishArticle(ctx, hfut.PublishArticleReq{
		UserID:   userID,
		Type:     3,            // 回答
		Title:    parent.Title, // 回答的 title 用父提问的，方便列表展示
		Content:  strings.TrimSpace(a.AnswerContent),
		ParentID: &pid,
	})
	if err != nil {
		zaplog.Logger.Errorf("autoReply hfut PublishArticle(answer) 失败 user=%d parent=%d: %v", userID, pid, err)
		return ackResult{
			Text: fmt.Sprintf("「%s」回答未发出，稍后再试", parent.Title),
			Kind: ackKindFail,
		}
	}
	return ackResult{
		Text: fmt.Sprintf("已答「%s」", parent.Title),
		Kind: ackKindSuccess,
	}
}

func dispatchCloseQuestion(ctx context.Context, key autoReplyBucketKey, userID uint, a kimi.RecognizeAction) ackResult {
	hint := strings.TrimSpace(a.CloseQuestionHint)
	openQs, err := global.Hfut.ListOpenQuestions(ctx, key.GroupID, 20)
	if err != nil {
		zaplog.Logger.Errorf("autoReply ListOpenQuestions(close) 失败 group=%d: %v", key.GroupID, err)
		return ackResult{Text: "关闭失败，稍后再试", Kind: ackKindFail}
	}
	// 只考虑这个 user 自己发布的提问；筛出后再匹配 hint
	mine := filterMyQuestions(openQs, userID)
	if len(mine) == 0 {
		return ackResult{Text: "没有进行中的求解答", Kind: ackKindAskUser}
	}
	var target *hfut.OpenQuestion
	if hint == "" && len(mine) == 1 {
		target = mine[0]
	} else if hint != "" {
		target = matchQuestion(mine, hint)
	}
	if target == nil {
		// P3.2：多个开放提问 + 用户没指明 → 存消歧上下文 + 编号反问。
		// 用户后续在 1 分钟内回 "1"/"2"/"①"/"②"，processSnapshot 会跳过 Kimi 直接走
		// dispatchDisambigChoice 完成关闭。
		candidates := make([]disambigCandidate, 0, len(mine))
		for _, q := range mine {
			candidates = append(candidates, disambigCandidate{ID: q.ID, Title: q.Title})
		}
		disambigMgr.Save(key, &disambigContext{
			Kind:       disambigKindCloseQuestion,
			UserID:     userID,
			GroupID:    key.GroupID,
			Candidates: candidates,
		})
		return ackResult{
			Text: "关哪条？回数字：\n" + formatNumberedCandidates(candidates),
			Kind: ackKindAskUser,
		}
	}
	if err := global.Hfut.CloseArticle(ctx, target.ID, userID); err != nil {
		zaplog.Logger.Errorf("autoReply hfut CloseArticle 失败 article=%d: %v", target.ID, err)
		return ackResult{
			Text: fmt.Sprintf("「%s」关闭失败，稍后再试", target.Title),
			Kind: ackKindFail,
		}
	}
	return ackResult{Text: fmt.Sprintf("已关「%s」", target.Title), Kind: ackKindSuccess}
}

// =============================================================================
// off_shelf — 下架商品
// =============================================================================

func dispatchOffShelf(ctx context.Context, key autoReplyBucketKey, userID uint, snap []autoReplyMsg, a kimi.RecognizeAction) ackResult {
	hint := strings.TrimSpace(a.OffShelfHint)

	// 0. **reply 精确定位**：用户在群里回复（reply）自己之前的上架消息说"已出"——
	//    bot 解析 reply.id 后调 hfut 反查 goods.bot_message_ids，命中即直接下架，
	//    跳过模糊匹配 + 多结果消歧。本通道仅对"用户消息含 reply 段"生效；reply.id
	//    指向的不是自己的上架消息（如 reply 别人的消息）会 (nil, nil) 平滑降级。
	if replyMsgID := firstReplyTargetMsgID(snap); replyMsgID != 0 {
		good, err := global.Hfut.LookupActiveGoodByMessageID(ctx, userID, replyMsgID)
		if err != nil {
			zaplog.Logger.Warnf("autoReply LookupActiveGoodByMessageID 失败 user=%d msg=%d: %v——降级到模糊匹配",
				userID, replyMsgID, err)
		} else if good != nil {
			zaplog.Logger.Infof("autoReply reply 命中 good=%d title=%q user=%d reply_msg=%d",
				good.ID, good.Title, userID, replyMsgID)
			if err := global.Hfut.OffShelfGood(ctx, good.ID, userID); err != nil {
				zaplog.Logger.Errorf("autoReply reply 路径 OffShelfGood 失败 good=%d: %v", good.ID, err)
				return ackResult{
					Text: fmt.Sprintf("「%s」下架失败，稍后再试", good.Title),
					Kind: ackKindFail,
				}
			}
			kindLabel := "二手"
			if good.Category == 2 {
				kindLabel = "求物品"
			}
			return ackResult{
				Text: fmt.Sprintf("已下架%s「%s」", kindLabel, good.Title),
				Kind: ackKindSuccess,
			}
		}
		// good == nil：reply 没指向自己的上架消息（可能 reply 别人的、可能 good 已下架），
		// 静默降级到下面的模糊匹配 / 消歧链路。
	}

	// hint 为空时优先看"最近一条上架"——用户的"不要了 / 不卖了"通常是指刚发的那条；
	// 命中即直接 OffShelfGood（cat=1 / cat=2 都按相同接口下架），文案区分"二手 / 求物品"。
	if hint == "" {
		if recent := recentGoodMgr.Peek(key); recent != nil && recent.HfutUserID == userID {
			if err := global.Hfut.OffShelfGood(ctx, recent.GoodID, userID); err != nil {
				zaplog.Logger.Warnf("autoReply 最近一条 OffShelfGood 失败 good=%d user=%d: %v——退化为列表匹配",
					recent.GoodID, userID, err)
				// 失败不直接 fail：可能商品已被别的路径下架，让下面的 ListActiveGoods 兜底
			} else {
				recentGoodMgr.Take(key)
				kindLabel := "二手"
				if recent.Category == 2 {
					kindLabel = "求物品"
				}
				show := orPlaceholder(recent.Title, "刚发的那条")
				return ackResult{
					Text: fmt.Sprintf("已撤销%s「%s」", kindLabel, show),
					Kind: ackKindSuccess,
				}
			}
		}
	}

	goods, err := global.Hfut.ListActiveGoods(ctx, userID, 20)
	if err != nil {
		zaplog.Logger.Errorf("autoReply ListActiveGoods 失败 user=%d: %v", userID, err)
		return ackResult{Text: "下架失败，稍后再试", Kind: ackKindFail}
	}
	if len(goods) == 0 {
		return ackResult{Text: "没有在售商品", Kind: ackKindAskUser}
	}
	// 单一在售直接下；多个 + 用户没指明 → 反问
	var target *hfut.ActiveGood
	if hint == "" && len(goods) == 1 {
		target = goods[0]
	} else if hint != "" {
		target = matchGood(goods, hint)
	}
	if target == nil {
		// P3.2：多个在售 + 用户没指明 → 存消歧上下文 + 编号反问。
		candidates := make([]disambigCandidate, 0, len(goods))
		for _, g := range goods {
			candidates = append(candidates, disambigCandidate{ID: g.ID, Title: g.Title})
		}
		disambigMgr.Save(key, &disambigContext{
			Kind:       disambigKindOffShelf,
			UserID:     userID,
			GroupID:    key.GroupID,
			Candidates: candidates,
		})
		return ackResult{
			Text: "下架哪件？回数字：\n" + formatNumberedCandidates(candidates),
			Kind: ackKindAskUser,
		}
	}
	if err := global.Hfut.OffShelfGood(ctx, target.ID, userID); err != nil {
		zaplog.Logger.Errorf("autoReply hfut OffShelfGood 失败 good=%d: %v", target.ID, err)
		return ackResult{
			Text: fmt.Sprintf("「%s」下架失败，稍后再试", target.Title),
			Kind: ackKindFail,
		}
	}
	return ackResult{Text: fmt.Sprintf("已下架「%s」", target.Title), Kind: ackKindSuccess}
}

// formatNumberedCandidates 把候选列表格式化成 "1. 标题 / 2. 标题" 多行文本。
//
// 单独抽出来：off_shelf 和 close_question 共用，避免文案不一致。
// 上限 5 条——用户在 QQ 群消息里看 5 条已经接近视觉上限，更多会被截断；
// 现实里同一卖家挂 5 件以上也极少见。超过 5 条只展示前 5 条 + 省略号提示。
func formatNumberedCandidates(cands []disambigCandidate) string {
	const maxShow = 5
	n := len(cands)
	if n > maxShow {
		n = maxShow
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d. %s", i+1, cands[i].Title)
	}
	if len(cands) > maxShow {
		fmt.Fprintf(&b, "\n…共%d条", len(cands))
	}
	return b.String()
}

// dispatchDisambigChoice 处理用户选了第 choice 个候选——按 disambig 上下文里
// 的 Kind 走对应的真"下架"/"关闭提问" 调用。
//
// choice 是 1-based；调用方应当先用 disambigChoiceFromText 验证过。
//
// 失败处理：候选越界 / hfut 调用失败均返回相应 ack；不会再二次反问让用户重选——
// 用户回错就让他重新发一句明确的话题。
func dispatchDisambigChoice(ctx context.Context, c *disambigContext, choice int) ackResult {
	if c == nil || choice < 1 || choice > len(c.Candidates) {
		return ackResult{
			Text: fmt.Sprintf("没有第 %d 条", choice),
			Kind: ackKindAskUser,
		}
	}
	cand := c.Candidates[choice-1]
	switch c.Kind {
	case disambigKindOffShelf:
		if err := global.Hfut.OffShelfGood(ctx, cand.ID, c.UserID); err != nil {
			zaplog.Logger.Errorf("autoReply 消歧→OffShelfGood 失败 good=%d: %v", cand.ID, err)
			return ackResult{
				Text: fmt.Sprintf("「%s」下架失败，稍后再试", cand.Title),
				Kind: ackKindFail,
			}
		}
		return ackResult{
			Text: fmt.Sprintf("已下架「%s」", cand.Title),
			Kind: ackKindSuccess,
		}
	case disambigKindCloseQuestion:
		if err := global.Hfut.CloseArticle(ctx, cand.ID, c.UserID); err != nil {
			zaplog.Logger.Errorf("autoReply 消歧→CloseArticle 失败 article=%d: %v", cand.ID, err)
			return ackResult{
				Text: fmt.Sprintf("「%s」关闭失败，稍后再试", cand.Title),
				Kind: ackKindFail,
			}
		}
		return ackResult{
			Text: fmt.Sprintf("已关「%s」", cand.Title),
			Kind: ackKindSuccess,
		}
	}
	return ackResult{Kind: ackKindIgnore}
}

// =============================================================================
// 文本模糊匹配 helpers
// =============================================================================

// matchQuestion 在候选提问列表里挑一个 title/content 包含 hint 关键词的；多条命中取最新一条。
//
// 当前是最简单的"hint 出现在 title 或 content 里"匹配——准确率够日常，
// 想做更精细的要 P3 阶段加 jaccard / token 相似度。
func matchQuestion(qs []*hfut.OpenQuestion, hint string) *hfut.OpenQuestion {
	if hint == "" {
		return nil
	}
	low := strings.ToLower(hint)
	for _, q := range qs {
		if strings.Contains(strings.ToLower(q.Title), low) ||
			strings.Contains(strings.ToLower(q.Content), low) {
			return q
		}
	}
	return nil
}

// firstReplyTargetMsgID 在快照里找第一个含 reply 段的消息，把它指向的被回复 message_id
// 返回（int64）。没有 reply 段 / 解析失败 / id == 0 时返回 0。
//
// 用例：用户在群里回复（QQ "回复消息"功能）自己之前 bot 上架成功的消息说"已出"——
// bot 拿到 reply.id 即可直接定位 good，跳过模糊匹配 + 反问消歧。
func firstReplyTargetMsgID(snap []autoReplyMsg) int64 {
	for _, m := range snap {
		for _, seg := range m.Segments {
			if seg.Type != "reply" {
				continue
			}
			rd, err := model.AsReplyData(seg.Data)
			if err != nil || rd.ID == "" {
				continue
			}
			id, err := strconv.ParseInt(strings.TrimSpace(rd.ID), 10, 64)
			if err != nil || id == 0 {
				continue
			}
			return id
		}
	}
	return 0
}

// collectBotMessageIDs 把本次上架"涉及到的所有 QQ message_id" 合并去重。
//
// 来源三个：
//  1. snap 里每条 autoReplyMsg.MessageID（外层 OneBot 群消息 ID）
//  2. action.SourceMessageIDs（Kimi 给出的"主文本来源" 消息 ID）
//  3. action.ImageMessageIDs（Kimi 给出的"关联图片来源"消息 ID）
//
// 三个来源大概率重叠（snap 已经包含全部），但模型给的两个字段是按"它认为属于本动作"
// 的子集；这里把"实际窗口里出现过的所有 message_id" 一起塞过去，确保后续用户 reply
// 任意一条都能反查到 good——比"只信模型"更稳。
//
// 上层 hfut.PublishGoodReq 会再做一次 dedup + 去 0；这里不重复做。
func collectBotMessageIDs(snap []autoReplyMsg, a kimi.RecognizeAction) []int64 {
	ids := make([]int64, 0, len(snap)+len(a.SourceMessageIDs)+len(a.ImageMessageIDs))
	for _, m := range snap {
		if m.MessageID != 0 {
			ids = append(ids, m.MessageID)
		}
	}
	ids = append(ids, a.SourceMessageIDs...)
	ids = append(ids, a.ImageMessageIDs...)
	return ids
}

// matchGood 同上，找 title 含 hint 的商品。
func matchGood(gs []*hfut.ActiveGood, hint string) *hfut.ActiveGood {
	if hint == "" {
		return nil
	}
	low := strings.ToLower(hint)
	for _, g := range gs {
		if strings.Contains(strings.ToLower(g.Title), low) {
			return g
		}
	}
	return nil
}

// filterMyQuestions 过滤出 user_id 自己发布的提问。
func filterMyQuestions(qs []*hfut.OpenQuestion, userID uint) []*hfut.OpenQuestion {
	out := make([]*hfut.OpenQuestion, 0, len(qs))
	for _, q := range qs {
		if q.UserID == userID {
			out = append(out, q)
		}
	}
	return out
}
