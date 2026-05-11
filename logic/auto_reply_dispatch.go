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
	"qq_bot/utils/hfut"
	"qq_bot/utils/kimi"
	"qq_bot/utils/metrics"
	zaplog "qq_bot/utils/zap"
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
func dispatchActionToHfut(
	ctx context.Context,
	key autoReplyBucketKey,
	userCard string,
	snap []autoReplyMsg,
	action kimi.RecognizeAction,
) ackResult {
	// 第 1 步：限流（P3.4）——仅对会"落库 / 改状态"的动作生效。
	// 反问类（off_shelf 多候选 / close_question 多候选）落到这里其实只是"反问 + 等回应"，
	// 也算一次 dispatch，但这一类不计数（避免用户被反问后立刻又触发限流）。
	if isMutatingAction(action.Type) {
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
	upsert, err := global.Hfut.UpsertQQChild(ctx, qqNumberOf(key.UserID), key.GroupID, userCard)
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
		return dispatchOffShelf(ctx, key, upsert.UserID, action)
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

// dispatchSeekGoods 处理「收/求/求购/收购 + 物品」（无价场景）：
//
//  1. 在本校在售二手里搜一下，命中则给"你是否在找…"的提示
//  2. 把求购意图作为 publish_good(category=2 求物品, price=0, negotiable=false) 落库——
//     让别的同学也能在 app 里看到。前端不展示价格，也不挂"有偿"tag。
//
// 两步合并成一条群里 ack；任一步失败不影响另一步该有的回执。
func dispatchSeekGoods(ctx context.Context, key autoReplyBucketKey, userID uint, snap []autoReplyMsg, a kimi.RecognizeAction) ackResult {
	title := strings.TrimSpace(a.SeekHint)
	if title == "" {
		return ackResult{Kind: ackKindIgnore}
	}

	// 第 1 步：检索现有在售
	hintLine := buildSeekMatchHint(ctx, key.GroupID, title)

	// 第 2 步：上架为「求物品」（cat=2、price=0、非面议——前端隐藏价格、不挂"有偿"tag）
	desc := strings.TrimSpace(a.Description)
	if desc == "" {
		desc = firstTextFromSnap(snap)
	}
	if desc == "" {
		desc = title
	}

	seekReq := hfut.PublishGoodReq{
		UserID:     userID,
		GroupID:    key.GroupID,
		Title:      title,
		Content:    desc,
		Category:   2,
		Negotiable: false,
		Price:      0,
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
			text := joinAckLines(hintLine, fmt.Sprintf("求物品「%s」已发过（id=%d）。回复 1=重复上架 / 2=下架旧的并上架", dupTitle, dup.ExistingID))
			return ackResult{Text: text, Kind: ackKindDup}
		}
		zaplog.Logger.Errorf("autoReply seek_goods PublishGood 失败 user=%d title=%q: %v", userID, title, pubErr)
		// 至少把搜索提示发出来，让用户拿到价值
		if hintLine != "" {
			return ackResult{Text: hintLine, Kind: ackKindSuccess}
		}
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

	pubLine := fmt.Sprintf("已发求物品「%s」，等同学在 app 内联系你", title)
	return ackResult{
		Text: joinAckLines(hintLine, pubLine),
		Kind: ackKindSuccess,
	}
}

// buildSeekMatchHint 取 SearchGoodsSeek 的第一条结果格式化成「你是否在找…」一行。
// 群没配学校 / 无命中 / 网络错都返回空串——调用方自行决定是否拼接。
func buildSeekMatchHint(ctx context.Context, groupID int64, q string) string {
	list, err := global.Hfut.SearchGoodsSeek(ctx, groupID, q, 5)
	if err != nil {
		if !errors.Is(err, hfut.ErrGroupNoSchool) {
			zaplog.Logger.Warnf("autoReply seek_goods 搜索失败 group=%d q=%q: %v", groupID, q, err)
		}
		return ""
	}
	if len(list) == 0 {
		return ""
	}
	g := list[0]
	created, perr := parseHFUTTime(g.CreatedAt)
	if perr != nil {
		zaplog.Logger.Warnf("autoReply seek_goods 解析 created_at=%q: %v", g.CreatedAt, perr)
		created = time.Now()
	}
	days := int(time.Since(created).Hours() / 24)
	if days < 0 {
		days = 0
	}
	// 价格展示与新规则对齐：
	//   - negotiable=true → "面议"
	//   - price>0       → "X 元"
	//   - price=0 非面议 → "免费"（cat=1 二手里偶尔有"白送"）
	var priceStr string
	switch {
	case g.Negotiable:
		priceStr = "面议"
	case g.Price > 0:
		priceStr = fmt.Sprintf("%g 元", float64(g.Price)/100)
	default:
		priceStr = "免费"
	}
	contact := "app内联系"
	if g.OrphanSeller && strings.TrimSpace(g.SellerQQ) != "" {
		contact = fmt.Sprintf("QQ：%s", strings.TrimSpace(g.SellerQQ))
	}
	return fmt.Sprintf("你是否在找「%s」，约 %d 天前上架，价格 %s，联系方式：%s",
		strings.TrimSpace(g.Title), days, priceStr, contact)
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

	// resp 里有 GoodID 用于"最近一条"快速查找；不在群里展示给用户——
	// 用户在 app "我的发布" 列表能看到刚发的，没必要再给个数字增加阅读负担。
	pubReq := hfut.PublishGoodReq{
		UserID:     userID,
		GroupID:    groupID,
		Title:      strings.TrimSpace(a.Title),
		Content:    strings.TrimSpace(a.Description),
		Category:   int16(a.Category),
		Negotiable: negotiable,
		Bargain:    a.Bargain,
		Price:      priceCents,
		Stock:      a.Stock, // <=0 时 hfut 后端按 1 兜底（详见 BotPublishGood 注释）
		Location:   strings.TrimSpace(a.Location),
		Images:     images,
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

	// 成功回执——只展示用户能看懂的字段：分类 / 标题 / 价格 / 地点 / 配图数。
	//
	// 价格展示规则（与前端 tag 规则对齐，用户视角）：
	//   - 二手 / 求物品：negotiable=true 显示"面议"
	//   - 二手 / 求物品：price>0 显示价格；cat=2 时额外加"（有偿）"
	//   - cat=2 + price=0 + 非面议：不展示价格（产品上前端也不展示）
	category := "二手"
	if a.Category == 2 {
		category = "求物品"
	}
	var b strings.Builder
	b.WriteString("已上架 ")
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
	// 数量（用户明说"出 N 个" 才显示；默认 1 不显示）
	if a.Stock > 1 {
		fmt.Fprintf(&b, " × %d", a.Stock)
	}
	if a.Location != "" {
		b.WriteString("，")
		b.WriteString(a.Location)
	}
	if len(images) > 0 {
		fmt.Fprintf(&b, "，配图 %d 张", len(images))
	}
	// 记录"最近一条"——给后续"不要了 / 不卖了"上下文化处理用
	if resp != nil {
		recentGoodMgr.Save(key, userID, resp.GoodID, strings.TrimSpace(a.Title), a.Category)
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
		Text: fmt.Sprintf("已发求解答「%s」",
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
		Type:     3, // 回答
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

func dispatchOffShelf(ctx context.Context, key autoReplyBucketKey, userID uint, a kimi.RecognizeAction) ackResult {
	hint := strings.TrimSpace(a.OffShelfHint)

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
