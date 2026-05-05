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
//	ackKindSuccess  真落库成功（含 goods_id/article_id）——任何模式都发
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
	"qq_bot/global"
	"qq_bot/model"
	"qq_bot/utils/hfut"
	"qq_bot/utils/kimi"
	zaplog "qq_bot/utils/zap"
	"strings"
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
	// 第 1 步：upsert 旗下账号——所有写操作都需要 user_id。
	upsert, err := global.Hfut.UpsertQQChild(ctx, qqNumberOf(key.UserID), key.GroupID, userCard)
	if err != nil {
		// 群没配学校 → 完全静默（按 SKILL.md 设计）
		if errors.Is(err, hfut.ErrGroupNoSchool) {
			zaplog.Logger.Debugf("autoReply group=%d user=%d 群没配学校，静默忽略 action %s",
				key.GroupID, key.UserID, action.Type)
			return ackResult{Kind: ackKindIgnore}
		}
		zaplog.Logger.Errorf("autoReply hfut UpsertQQChild 失败 group=%d user=%d: %v", key.GroupID, key.UserID, err)
		return ackResult{Text: "[bot] 系统繁忙（账号同步失败），稍后再来", Kind: ackKindFail}
	}
	if upsert.Created {
		zaplog.Logger.Infof("autoReply 创建旗下账号 group=%d qq=%d → user_id=%d school_id=%d",
			key.GroupID, key.UserID, upsert.UserID, upsert.SchoolID)
	}

	// 第 2 步：按 action.Type 分流到具体 hfut 调用。
	switch action.Type {
	case "publish_good":
		return dispatchPublishGood(ctx, upsert.UserID, snap, action)
	case "publish_question":
		return dispatchPublishQuestion(ctx, upsert.UserID, snap, action)
	case "publish_answer":
		return dispatchPublishAnswer(ctx, key.GroupID, upsert.UserID, action)
	case "off_shelf":
		return dispatchOffShelf(ctx, upsert.UserID, action)
	case "close_question":
		return dispatchCloseQuestion(ctx, key.GroupID, upsert.UserID, action)
	default:
		// 未知 type 不该走到这里（processSnapshot 那边已经过滤过 none）
		return ackResult{Kind: ackKindIgnore}
	}
}

// qqNumberOf 把 NapCat 的 user_id（int64）转成 qq_number 字符串，
// 跟 hfut 那边 user.qq_number 字段对齐（varchar）。
func qqNumberOf(userID int64) string {
	return fmt.Sprintf("%d", userID)
}

// =============================================================================
// publish_good — 上架商品
// =============================================================================

func dispatchPublishGood(ctx context.Context, userID uint, snap []autoReplyMsg, a kimi.RecognizeAction) ackResult {
	// 用 ImageMessageIDs 还原图片 URL（NapCat 临时 URL；图片有效期问题 P1.4b 阶段做 OSS 转存）
	images := imageURLsFromSnap(snap, a.ImageMessageIDs)

	// price: nil/Negotiable=true → 0 + Negotiable=true，hfut 那边按 negotiable 跳过 price
	priceCents := 0
	negotiable := a.Negotiable || a.Price == nil
	if !negotiable && a.Price != nil {
		priceCents = int(*a.Price * 100) // 元 → 分
	}

	resp, err := global.Hfut.PublishGood(ctx, hfut.PublishGoodReq{
		UserID:     userID,
		Title:      strings.TrimSpace(a.Title),
		Content:    strings.TrimSpace(a.Description),
		Category:   int16(a.Category),
		Negotiable: negotiable,
		Price:      priceCents,
		Location:   strings.TrimSpace(a.Location),
		Images:     images,
	})
	if err != nil {
		// 去重保护命中——不视为失败，给用户友好提示，不重复上架
		var dup *hfut.DuplicateGoodInfo
		if errors.As(err, &dup) {
			zaplog.Logger.Infof("autoReply 去重命中 user=%d title=%q existing=%d/%q",
				userID, a.Title, dup.ExistingID, dup.ExistingTitle)
			if dup.ExistingTitle != "" {
				return ackResult{
					Text: fmt.Sprintf("你最近已经发过类似的「%s」（goods_id=%d），没有重复上架；如要重发请先回复'下架旧的'",
						dup.ExistingTitle, dup.ExistingID),
					Kind: ackKindDup,
				}
			}
			return ackResult{
				Text: "你最近已经发过类似的商品，没有重复上架；如要重发请先回复'下架旧的'",
				Kind: ackKindDup,
			}
		}
		zaplog.Logger.Errorf("autoReply hfut PublishGood 失败 user=%d title=%q: %v", userID, a.Title, err)
		return ackResult{
			Text: fmt.Sprintf("已识别到「%s」，但同步到 app 失败了，稍后再试", orPlaceholder(a.Title, "(无标题)")),
			Kind: ackKindFail,
		}
	}

	// 成功回执：含商品 id；不带 url 因为前端 deep link 还没设计
	category := "二手"
	if a.Category == 2 {
		category = "有偿求助"
	}
	priceStr := "面议"
	if !negotiable {
		priceStr = fmt.Sprintf("%g 元", *a.Price)
	}
	imgPart := ""
	if len(images) > 0 {
		imgPart = fmt.Sprintf("，图×%d", len(images))
	}
	locPart := ""
	if a.Location != "" {
		locPart = "，地点 " + a.Location
	}
	return ackResult{
		Text: fmt.Sprintf("已为你上架%s「%s」%s%s%s（goods_id=%d）",
			category, orPlaceholder(a.Title, "(无标题)"), "："+priceStr, locPart, imgPart, resp.GoodID),
		Kind: ackKindSuccess,
	}
}

// imageURLsFromSnap 按 message_id 在 snap 里找回原 segments，提取图片 URL。
//
// 当前直接返回 NapCat 临时 URL——hfut 那边会原样存进 goods.images。
// **TODO P1.4b**：bot 起一个内部 OSS 上传链路（先下载 NapCat 图，再上传到 hfut OSS），
// 把"过期 URL"问题永久解决。短期内 NapCat URL 一般有效期足够 app 端访问，先这样跑。
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

// =============================================================================
// publish_question / publish_answer / close_question — 提问 + 回答
// =============================================================================

func dispatchPublishQuestion(ctx context.Context, userID uint, snap []autoReplyMsg, a kimi.RecognizeAction) ackResult {
	resp, err := global.Hfut.PublishArticle(ctx, hfut.PublishArticleReq{
		UserID:  userID,
		Type:    2, // 提问
		Title:   strings.TrimSpace(a.QuestionTitle),
		Content: strings.TrimSpace(orPlaceholder(a.QuestionContent, a.QuestionTitle)), // content 兜底用 title
		Images:  imageURLsFromSnap(snap, a.ImageMessageIDs),
	})
	if err != nil {
		zaplog.Logger.Errorf("autoReply hfut PublishArticle(question) 失败 user=%d: %v", userID, err)
		return ackResult{
			Text: fmt.Sprintf("已识别到提问「%s」，但同步到 app 失败了，稍后再试",
				orPlaceholder(a.QuestionTitle, "(未识别标题)")),
			Kind: ackKindFail,
		}
	}
	return ackResult{
		Text: fmt.Sprintf("已为你发布提问「%s」（article_id=%d）",
			orPlaceholder(a.QuestionTitle, "(未识别标题)"), resp.ArticleID),
		Kind: ackKindSuccess,
	}
}

func dispatchPublishAnswer(ctx context.Context, groupID int64, userID uint, a kimi.RecognizeAction) ackResult {
	hint := strings.TrimSpace(a.AnswerHintTo)
	if hint == "" {
		return ackResult{
			Text: "已识别到你想回答群里某条提问，但没说清楚是哪条，请明确提到关键词后再发",
			Kind: ackKindAskUser,
		}
	}
	openQs, err := global.Hfut.ListOpenQuestions(ctx, groupID, 20)
	if err != nil {
		zaplog.Logger.Errorf("autoReply ListOpenQuestions 失败 group=%d: %v", groupID, err)
		return ackResult{Text: "已识别到你想回答某条提问，但同步到 app 失败了，稍后再试", Kind: ackKindFail}
	}
	parent := matchQuestion(openQs, hint)
	if parent == nil {
		return ackResult{
			Text: fmt.Sprintf("已识别到你想回答「%s」，但 app 里没找到对应的提问；可能那条提问还没被同步", hint),
			Kind: ackKindAskUser,
		}
	}
	pid := int(parent.ID)
	resp, err := global.Hfut.PublishArticle(ctx, hfut.PublishArticleReq{
		UserID:   userID,
		Type:     3, // 回答
		Title:    parent.Title, // 回答的 title 用父提问的，方便列表展示
		Content:  strings.TrimSpace(a.AnswerContent),
		ParentID: &pid,
	})
	if err != nil {
		zaplog.Logger.Errorf("autoReply hfut PublishArticle(answer) 失败 user=%d parent=%d: %v", userID, pid, err)
		return ackResult{
			Text: fmt.Sprintf("已识别到你想回答「%s」，但同步到 app 失败了，稍后再试", parent.Title),
			Kind: ackKindFail,
		}
	}
	return ackResult{
		Text: fmt.Sprintf("已为你提交对「%s」的回答（article_id=%d）", parent.Title, resp.ArticleID),
		Kind: ackKindSuccess,
	}
}

func dispatchCloseQuestion(ctx context.Context, groupID int64, userID uint, a kimi.RecognizeAction) ackResult {
	hint := strings.TrimSpace(a.CloseQuestionHint)
	openQs, err := global.Hfut.ListOpenQuestions(ctx, groupID, 20)
	if err != nil {
		zaplog.Logger.Errorf("autoReply ListOpenQuestions(close) 失败 group=%d: %v", groupID, err)
		return ackResult{Text: "已识别到你想关闭提问，但同步到 app 失败了，稍后再试", Kind: ackKindFail}
	}
	// 只考虑这个 user 自己发布的提问；筛出后再匹配 hint
	mine := filterMyQuestions(openQs, userID)
	if len(mine) == 0 {
		return ackResult{Text: "你目前在 app 里没有开放中的提问可关闭", Kind: ackKindAskUser}
	}
	var target *hfut.OpenQuestion
	if hint == "" && len(mine) == 1 {
		target = mine[0]
	} else if hint != "" {
		target = matchQuestion(mine, hint)
	}
	if target == nil {
		// 多个开放提问 + 用户没指明 → 反问
		var titles []string
		for _, q := range mine {
			titles = append(titles, "「"+q.Title+"」")
		}
		return ackResult{
			Text: fmt.Sprintf("你最近在挂这几条提问：%s，请明确说要关闭哪一条", strings.Join(titles, "、")),
			Kind: ackKindAskUser,
		}
	}
	if err := global.Hfut.CloseArticle(ctx, target.ID, userID); err != nil {
		zaplog.Logger.Errorf("autoReply hfut CloseArticle 失败 article=%d: %v", target.ID, err)
		return ackResult{
			Text: fmt.Sprintf("已识别到你想关闭「%s」，但同步到 app 失败了，稍后再试", target.Title),
			Kind: ackKindFail,
		}
	}
	return ackResult{Text: fmt.Sprintf("已为你关闭提问「%s」", target.Title), Kind: ackKindSuccess}
}

// =============================================================================
// off_shelf — 下架商品
// =============================================================================

func dispatchOffShelf(ctx context.Context, userID uint, a kimi.RecognizeAction) ackResult {
	hint := strings.TrimSpace(a.OffShelfHint)
	goods, err := global.Hfut.ListActiveGoods(ctx, userID, 20)
	if err != nil {
		zaplog.Logger.Errorf("autoReply ListActiveGoods 失败 user=%d: %v", userID, err)
		return ackResult{Text: "已识别到你想下架商品，但同步到 app 失败了，稍后再试", Kind: ackKindFail}
	}
	if len(goods) == 0 {
		return ackResult{Text: "你目前在 app 里没有在售商品可下架", Kind: ackKindAskUser}
	}
	// 单一在售直接下；多个 + 用户没指明 → 反问
	var target *hfut.ActiveGood
	if hint == "" && len(goods) == 1 {
		target = goods[0]
	} else if hint != "" {
		target = matchGood(goods, hint)
	}
	if target == nil {
		var titles []string
		for _, g := range goods {
			titles = append(titles, "「"+g.Title+"」")
		}
		return ackResult{
			Text: fmt.Sprintf("你最近在挂这几件：%s，请明确说要下架哪一个", strings.Join(titles, "、")),
			Kind: ackKindAskUser,
		}
	}
	if err := global.Hfut.OffShelfGood(ctx, target.ID, userID); err != nil {
		zaplog.Logger.Errorf("autoReply hfut OffShelfGood 失败 good=%d: %v", target.ID, err)
		return ackResult{
			Text: fmt.Sprintf("已识别到你想下架「%s」，但同步到 app 失败了，稍后再试", target.Title),
			Kind: ackKindFail,
		}
	}
	return ackResult{Text: fmt.Sprintf("已为你下架「%s」", target.Title), Kind: ackKindSuccess}
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
