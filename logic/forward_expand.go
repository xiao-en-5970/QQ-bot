// 处理 NapCat 群消息里的 "聊天记录" / 合并转发 (forward) segment：
//
// 群里的「聊天记录」从客户端发出去后，在 OneBot11 的事件里只剩**一条** forward
// segment：
//
//   {"type":"forward","data":{"id":"7234567890123456789"}}
//
// 真正的子消息列表存放在 NapCat 侧的资源里，需要主动调 /get_forward_msg 拿。
// 拿到后我们把每条子消息合成"伪 group_message 事件"，按子消息顺序依次喂回
// autoReplyMgr.Push，于是窗口聚合 + 三态切分 (auto_reply_unit_split.go) 会自然把
// "聊天记录里多条上架文字 + 配图" 当作正常对话识别。
//
// 关键决策：
//   - **bucket key 用转发者的 (groupID, userID)，不用内层 sender**
//     —— 内层 sender 可能根本不在这个群里，给他建桶会污染对未来本群消息的判断；
//        而所有展开的子消息都属于"这位用户的一次发布行为"。
//   - **递归展开**：聊天记录里又嵌聊天记录的情况合法但少见，限深 3 层防爆。
//   - **失败兜底**：fetch 失败时不抛错——把这条 forward 当成 [合并转发] 占位符
//     交给原 flatten 路径走（即维持当前的"不展开"行为，不影响其它消息）。
//   - **异步**：fetch + 多次 Push 必须放协程里执行，否则会阻塞 wsclient 读循环。

package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
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
			zaplog.Logger.Warnf("forward segment 解析失败 group=%d msgid=%d: %v", msg.GroupID, msg.MessageID, parseErr)
			// 解析失败：退化成原 segment 保留（让外层 flatten 走默认 [forward]）
			nonForwardBuf = append(nonForwardBuf, seg)
			continue
		}

		nodes, fetchErr := fetchForwardNodes(client, fwd.ID)
		if fetchErr != nil {
			zaplog.Logger.Warnf("get_forward_msg 失败 group=%d msgid=%d fid=%s: %v",
				msg.GroupID, msg.MessageID, fwd.ID, fetchErr)
			nonForwardBuf = append(nonForwardBuf, seg) // 当成 [forward] 占位降级
			continue
		}
		if len(nodes) == 0 {
			zaplog.Logger.Debugf("get_forward_msg 返回空 group=%d msgid=%d fid=%s",
				msg.GroupID, msg.MessageID, fwd.ID)
			// 空聊天记录：丢掉这条 forward 段（不留占位，避免把"空"误传给 Kimi）
			continue
		}

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

	// node.time 是内层消息的发送时间，更接近"用户那一刻发布"的语义；
	// 但 autoReplyMgr.Push 内部用 time.Now() 做入桶时间，msg.Time 只在日志里展示，
	// 所以这里如果有内层 time 就用，没有就保留外层时间。
	if node.Data.Time > 0 {
		out.Time = node.Data.Time
	}

	// 给伪消息一个稳定的、与外层冲突小的 MessageID：外层 ID 高位 + 节点 idx 低位。
	// 不影响 LRU 去重（HandleAtMessage 顶部的 ProcessedMsgIDs 不会再处理这些伪消息，
	// 因为我们绕过了它，直接进 Push）。
	out.MessageID = composePseudoMsgID(forwarder.MessageID, idx)
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
func fetchForwardNodes(client *http.Client, forwardID string) ([]model.ForwardNode, error) {
	req := &model.GetForwardMsgReq{MessageID: forwardID, ID: forwardID}
	err, resp := service.GetForwardMsg(client, req)
	if err != nil {
		return nil, err
	}
	return resp.Data.Messages, nil
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

	// 注意：Push 是 O(1) 入桶，但每追加一条都会触发 splitBucketIntoUnits 重切。
	// 默认 maxSize=20，若展开后超出，会被切成多个 unit 分别识别——对结果质量影响
	// 不大（Kimi 会被调多次），且实际聊天记录里上架内容很少超过 20 条，先按这个走。
	for _, pseudo := range expanded {
		autoReplyMgr.Push(pseudo.GroupID, pseudo.UserID, strings.TrimSpace(pseudo.Sender.Nickname), pseudo)
	}
}

// newForwardFetchClient 给 forward fetch 用的临时 client。
// 简化处理：写死 10s timeout —— NapCat /get_forward_msg 走的是本地连接（同台机器或
// 内网），正常 1-2s 完成；10s 已经留足兜底余量。
func newForwardFetchClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}
