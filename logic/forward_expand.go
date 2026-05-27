// 处理 NapCat 群消息里的 "聊天记录" / 合并转发 (forward) segment：
//
// 群里的「聊天记录」从客户端发出去后，在 OneBot11 的事件里只剩**一条** forward
// segment：
//
//   {"type":"forward","data":{"id":"7234567890123456789"}}
//
// 真正的子消息列表存放在 NapCat 侧的资源里，需要主动调 /get_forward_msg 拿。
//
// **策略：合并而非展开**（v2，2026-05-25）
//
// 拿到 N 个子节点后，**把所有子节点的 segments 平铺拼接成一条消息**（保留转发者
// 的 GroupID / UserID / MessageID / Time），再 Push 进桶——Kimi 把它当作"一条
// 图文同条的批量上架消息"识别为单个综合 publish_good，所有图都归这一个商品。
//
// 旧策略（v1）是把每条子消息单独 Push 进桶并打 [来自聊天记录] 标记，让 Kimi 走
// "每条独立判定"分支；实测在"图条 / 文条交替"模式下，每条记录单独看图文不成对，
// 导致所有商品 image_message_ids 都是空（图都在隔壁条但规则禁止跨条混入）。
// 用户决定改用合并策略：把聊天记录视作"一个综合商品 + 所有图"，简化又稳定。
//
// 关键决策：
//   - **bucket key 用转发者的 (groupID, userID)，不用内层 sender**
//     —— 内层 sender 可能根本不在这个群里，给他建桶会污染对未来本群消息的判断；
//        而所有展开的子消息都属于"这位用户的一次发布行为"。
//   - **递归扁平化**：聊天记录里又嵌聊天记录的情况合法但少见，限深 3 层防爆，
//     超过深度则那一层的 forward 段保留为占位符。
//   - **失败兜底**：fetch 失败时不抛错——把原消息以"原样"Push 一次（forward
//     段退化为 [合并转发(未展开)] 占位符），不丢消息。
//   - **异步**：fetch + Push 必须放协程里执行，否则会阻塞 wsclient 读循环。

package logic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"qq_bot/conf"
	"qq_bot/model"
	zaplog "qq_bot/utils/zap"
	"strings"
	"time"
)

// maxForwardExpandDepth 限制嵌套聊天记录的展开深度（A 转发了 B 转发的聊天记录…）。
// 一般 1-2 层足够，给到 3 留余量；继续嵌套则那层 forward 保留原样不展开。
const maxForwardExpandDepth = 3

// containsForwardSegment 是否含至少一条 forward segment（不递归检查嵌套）。
func containsForwardSegment(msg *model.Message) bool {
	if msg == nil {
		return false
	}
	for _, seg := range msg.Message {
		if seg.Type == "forward" {
			return true
		}
	}
	return false
}

// expandForwardInGroupMsg 把一条群消息里所有 forward segment 展开成多条子消息。
//
// 返回的 []*model.Message 已经按聊天记录里的时间顺序排列。每条子消息：
//   - GroupID = 原消息的 GroupID
//   - UserID  = 原消息的转发者 UserID（不是内层 sender）
//   - Sender  = 转发者的 Sender（保持原 nickname）
//   - Message = 内层 node 的 segments（如果 node 内还有 forward 段会被进一步递归展开）
//   - Time / MessageID 来自内层 node；MessageID 不可用时 fallback 为 0
//
// 一条消息里可能混合 "若干文字段 + 一个 forward 段" —— 这种情况下我们把文字段
// 保留为第一条伪消息，forward 展开的内容跟在后面。
//
// 调用方：handleAutoReply 异步处理（这里有网络 IO）。
func expandForwardInGroupMsg(client *http.Client, msg *model.Message) []*model.Message {
	return expandForwardInGroupMsgWithDepth(client, msg, 0)
}

func expandForwardInGroupMsgWithDepth(client *http.Client, msg *model.Message, depth int) []*model.Message {
	if msg == nil || len(msg.Message) == 0 {
		return nil
	}
	if depth >= maxForwardExpandDepth {
		// 触到深度上限：当作普通消息保留原样，外层把 forward 段降级为 [forward]
		// 占位符（由 flattenMessageText / buildRecognizeInput 处理）。
		return []*model.Message{msg}
	}

	var out []*model.Message

	// nonForwardBuf 累积当前消息里 forward 段之外的 segments，遇到 forward 段时
	// 先把累积的非 forward 段作为一条伪消息推出去（保留用户在转发前后写的解释文字）。
	var nonForwardBuf []model.MessageSegment
	flushNonForward := func() {
		if len(nonForwardBuf) == 0 {
			return
		}
		clone := *msg
		clone.Message = nonForwardBuf
		out = append(out, &clone)
		nonForwardBuf = nil
	}

	for _, seg := range msg.Message {
		if seg.Type != "forward" {
			nonForwardBuf = append(nonForwardBuf, seg)
			continue
		}

		fwd, parseErr := model.AsForwardData(seg.Data)
		if parseErr != nil || fwd.ID == "" {
			zaplog.Logger.Warnf("forward segment 解析失败 group=%d msgid=%d raw=%+v err=%v",
				msg.GroupID, msg.MessageID, seg.Data, parseErr)
			// 解析失败：退化成原 segment 保留（让外层 flatten 走默认 [forward]）
			nonForwardBuf = append(nonForwardBuf, seg)
			continue
		}
		zaplog.Logger.Infof("forward fetch 开始 group=%d msgid=%d fid=%s",
			msg.GroupID, msg.MessageID, fwd.ID)

		nodes, fetchErr := fetchForwardNodes(client, fwd.ID)
		if fetchErr != nil {
			zaplog.Logger.Warnf("get_forward_msg 失败 group=%d msgid=%d fid=%s: %v",
				msg.GroupID, msg.MessageID, fwd.ID, fetchErr)
			nonForwardBuf = append(nonForwardBuf, seg) // 当成 [forward] 占位降级
			continue
		}
		if len(nodes) == 0 {
			// NapCat 这条 fid 拿不到子消息（可能是 forward 资源已过期 / NapCat 没缓存）
			// → 保留原 segment 让外层 flatten 走 [合并转发(未展开)] 占位，不丢消息
			zaplog.Logger.Warnf("get_forward_msg 返回 0 子消息（fid 可能过期或 NapCat 未缓存）group=%d msgid=%d fid=%s",
				msg.GroupID, msg.MessageID, fwd.ID)
			nonForwardBuf = append(nonForwardBuf, seg)
			continue
		}
		zaplog.Logger.Infof("forward fetch 成功 group=%d msgid=%d fid=%s nodes=%d",
			msg.GroupID, msg.MessageID, fwd.ID, len(nodes))

		// 先把当前消息里 forward 之前的非 forward 段刷出去
		flushNonForward()

		// 按节点顺序依次展开
		for i, node := range nodes {
			pseudo := pseudoMessageFromNode(msg, &node, i)
			if pseudo == nil {
				continue
			}
			// 若节点里又含 forward → 再递归一层
			if containsForwardSegment(pseudo) {
				out = append(out, expandForwardInGroupMsgWithDepth(client, pseudo, depth+1)...)
			} else {
				out = append(out, pseudo)
			}
		}
	}

	// 把消息尾部还没刷的非 forward 段刷出去
	flushNonForward()
	return out
}

// pseudoMessageFromNode 把一条 forward 子节点合成"伪 group_message 事件"。
//
// 关键：UserID / Sender 都用**外层转发者**的，不是内层 sender。
// 详见文件头部的 bucket key 设计说明。
func pseudoMessageFromNode(forwarder *model.Message, node *model.ForwardNode, idx int) *model.Message {
	segs := node.EffectiveSegments()
	if len(segs) == 0 {
		return nil
	}

	out := *forwarder
	out.Message = segs

	// 时间：用 node 真实发送时间（扁平格式 FlatTime 或嵌套 Data.Time），让 Kimi 看到
	// 节点间真实间隔；node 没给时间时保留外层时间。
	if t := node.EffectiveTime(); t > 0 {
		out.Time = t
	}

	// MessageID：优先用扁平格式带的真实 inner message_id（NapCat 实测会给出
	// FlatMessageID，是内层消息在 QQ 端的真实 ID）；没拿到 fallback 到伪 ID
	// （外层 ID 高位 + idx 低位）。
	if node.FlatMessageID != 0 {
		out.MessageID = node.FlatMessageID
	} else {
		out.MessageID = composePseudoMsgID(forwarder.MessageID, idx)
	}
	return &out
}

func composePseudoMsgID(outerID int64, idx int) int64 {
	// 把外层 MessageID 左移 8 位再叠加 idx，让伪 ID 和真实 ID 在 log 里好辨认（末两位 hex 是 idx）。
	// idx 不会超过 256（一次合并转发不会有那么多节点），这里 cap 一下足够。
	if idx > 0xFF {
		idx = 0xFF
	}
	return (outerID << 8) | int64(idx)
}

// fetchForwardNodes 调 NapCat /get_forward_msg 拿子节点列表。
//
// 这里有 1 次额外的 HTTP 调用——业界正常的合并转发拉取，正常 1-2s 完成。
// 调用方应放在 goroutine 里执行以免阻塞 wsclient。
//
// 失败 / 0 子消息时返回值：err != nil 或 len(nodes)==0；调用方负责降级。
// 返回 0 个子消息的常见原因：
//   - forward 资源 ID 已过期（QQ 端清理 / 跨群转发 token 失效）
//   - NapCat 版本响应字段名不同（messages / nodes / content）→ 见 fetchForwardNodesRaw
//   - 用户转发的不是"聊天记录"而是"单条转发" → 内容缺失
//
// 实现细节：绕过 service.BaseService，直接发请求 + 读 raw body，能 dump 完整响应
// 并兼容 messages / nodes / content 三种字段名（不同 NapCat 版本响应字段不一致）。
func fetchForwardNodes(client *http.Client, forwardID string) ([]model.ForwardNode, error) {
	return fetchForwardNodesRaw(client, forwardID)
}

// fetchForwardNodesRaw 显式手写请求 + 解析，方便排查。
func fetchForwardNodesRaw(client *http.Client, forwardID string) ([]model.ForwardNode, error) {
	body, _ := json.Marshal(map[string]string{
		"message_id": forwardID,
		"id":         forwardID,
	})
	url := conf.Cfg.Server.Address + "get_forward_msg"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("forward fetch: build req: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if tok := conf.Cfg.Server.AccessToken; tok != "" {
		httpReq.Header.Set("Authorization", "Bearer "+tok)
	}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("forward fetch: %w", err)
	}
	defer httpResp.Body.Close()
	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("forward fetch: read body: %w", err)
	}
	// raw body 截断 800 字 dump 出来——线上聊天记录场景频次很低，info 级别诊断够用
	zaplog.Logger.Infof("get_forward_msg raw fid=%s http=%d body=%s",
		forwardID, httpResp.StatusCode, truncateRawForward(string(raw), 800))

	// 同时支持 NapCat 三种可能的字段名：messages / nodes / content
	var envelope struct {
		Status  string          `json:"status"`
		RetCode int             `json:"retcode"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("forward fetch: parse envelope: %w", err)
	}
	if envelope.RetCode != 0 && envelope.Status != "ok" {
		return nil, fmt.Errorf("forward fetch: napcat status=%s retcode=%d msg=%s",
			envelope.Status, envelope.RetCode, envelope.Message)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, nil
	}
	var inner struct {
		Messages []model.ForwardNode `json:"messages"`
		Nodes    []model.ForwardNode `json:"nodes"`
		Content  []model.ForwardNode `json:"content"`
	}
	if err := json.Unmarshal(envelope.Data, &inner); err != nil {
		return nil, fmt.Errorf("forward fetch: parse data: %w", err)
	}
	switch {
	case len(inner.Messages) > 0:
		return inner.Messages, nil
	case len(inner.Nodes) > 0:
		return inner.Nodes, nil
	case len(inner.Content) > 0:
		return inner.Content, nil
	}
	return nil, nil
}

func truncateRawForward(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// expandAndPushForward 接管"消息含 forward 段"的群消息，异步展开后按时间顺序
// 逐条 Push 进同一桶。
//
// 把"展开 + Push"做成一个独立函数：
//   - handleAutoReply 那边只负责"是否含 forward"的判断 + 起 goroutine
//   - 错误兜底集中在这里
//
// 即便所有 forward 展开后没拿到任何子消息（fetch 全失败），也要把原消息以"原样"
// 兜底 Push 一次——避免完全吞掉用户这一条群消息，让 Kimi 至少能看到 [forward]
// 占位符。
func expandAndPushForward(msg *model.Message) {
	start := time.Now()
	client := newForwardFetchClient()
	expanded := expandForwardInGroupMsg(client, msg)
	if len(expanded) == 0 {
		// 全部 fetch 失败的兜底：原消息直接 Push（让 forward 段以占位符形式参与识别）
		autoReplyMgr.Push(msg.GroupID, msg.UserID, msg.Sender.Nickname, msg)
		zaplog.Logger.Warnf("forward 展开为空，按原消息 Push group=%d msgid=%d", msg.GroupID, msg.MessageID)
		return
	}

	zaplog.Logger.Infof("forward 展开 group=%d msgid=%d nodes=%d cost=%s",
		msg.GroupID, msg.MessageID, len(expanded), time.Since(start))

	// Push 入桶后等 300s 沉默才整体送 Kimi 识别。整段聊天记录用 PushBatchFromForward
	// **一次过**——避免被桶的 maxSize=20 上限切碎（30 节点的聊天记录如果逐条 push
	// 会在第 20 条触发 size flush、剩下 10 条变成第二个 snapshot，那样 Kimi 就会把
	// 一段聊天记录识别成两个独立的批量上架，违背语义）。详见 recognizeSystemPrompt
	// "聊天记录展开"节 + skill/bot/recognition.md "批量上架"段。
	pseudoMsgs := make([]*model.Message, 0, len(expanded))
	originTimes := make([]time.Time, 0, len(expanded))
	var firstGroupID, firstUserID int64
	var firstNickname string
	for i, pseudo := range expanded {
		if i == 0 {
			firstGroupID = pseudo.GroupID
			firstUserID = pseudo.UserID
			firstNickname = strings.TrimSpace(pseudo.Sender.Nickname)
		}
		// node 的 Time 是 unix 秒；零值 / 负数都视为"没拿到"，让 Push 内部 fallback
		// 到入桶时间。
		var originTime time.Time
		if pseudo.Time > 0 {
			originTime = time.Unix(pseudo.Time, 0)
		}
		pseudoMsgs = append(pseudoMsgs, pseudo)
		originTimes = append(originTimes, originTime)
	}
	autoReplyMgr.PushBatchFromForward(firstGroupID, firstUserID, firstNickname, pseudoMsgs, originTimes)
}

// newForwardFetchClient 给 forward fetch 用的临时 client。
// 简化处理：写死 10s timeout —— NapCat /get_forward_msg 走的是本地连接（同台机器或
// 内网），正常 1-2s 完成；10s 已经留足兜底余量。
func newForwardFetchClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}
