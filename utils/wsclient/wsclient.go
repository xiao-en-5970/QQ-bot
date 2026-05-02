// Package wsclient 维护一条到 NapCat 的 OneBot11 WebSocket 长连接，
// 把收到的群消息事件交给 logic.HandleAtMessage 处理。
//
// 为什么不再轮询 get_group_msg_history：
//
//	NapCat 在某些群上 get_group_msg_history 返回的"最新 20 条"会一直停在 bot 启动那一刻
//	看到的状态，新到的消息不会被纳入返回（详见 logic/get_new_at_message.go 注释里的踩坑记录）。
//	WebSocket 是 NapCat 内部 push 的事件流，新消息一进 NapCat 就会被推过来，绕过那个 cache。
//
// 协议要点（OneBot11 标准 + NapCat 扩展）：
//   - 服务端是 NapCat（websocketServers 配置开启），bot 作为客户端 dial 过去
//   - 鉴权头 `Authorization: Bearer <access_token>`，token 留空则不带
//   - 服务端推 JSON 文本帧；我们只关心 post_type=message && message_type=group
//   - 其它事件（meta_event 心跳、notice、API 响应回包、private message 等）一律忽略
package wsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/model"
	"qq_bot/utils/client_pool"
	zaplog "qq_bot/utils/zap"
	"time"

	"github.com/gorilla/websocket"
)

// Run 启动 WebSocket 监听协程。会一直运行直到 ctx 被取消。
//
// 断线 / 网络抖动时会自动重连，指数退避（1s, 2s, 4s, ... 上限 30s），
// 一旦成功建立连接就把退避重置回 1s。
func Run(ctx context.Context) {
	global.Wg.Add(1)
	defer global.Wg.Done()
	zaplog.Logger.Debugf("协程WSListener启动")
	defer zaplog.Logger.Debugf("协程WSListener退出")

	const minBackoff = time.Second
	const maxBackoff = 30 * time.Second
	backoff := minBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		err := connectAndListen(ctx)
		if ctx.Err() != nil {
			return
		}
		zaplog.Logger.Errorf("WebSocket 连接断开: %v, %v 后重连", err, backoff)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// connectAndListen 跑一次连接：dial → 鉴权 → 读循环。
// 任何错误（含 EOF）都会返回，让外层 Run 走重连。
// ctx 被取消时返回 nil（外层据此判断是否还要重连）。
func connectAndListen(ctx context.Context) error {
	url := conf.Cfg.Server.WSAddress
	if url == "" {
		return fmt.Errorf("server.ws_address 未配置，无法连接 WebSocket")
	}

	// WS 鉴权 token：NapCat 把 HTTP / WS 当成两套独立的网络适配器，
	// 两边的 token 可以不同。优先使用专门的 WSAccessToken；留空时回退到 HTTP 那个，
	// 方便"两端用同一个 token"的简单场景一个变量搞定。
	header := http.Header{}
	tok := conf.Cfg.Server.WSAccessToken
	if tok == "" {
		tok = conf.Cfg.Server.AccessToken
	}
	if tok != "" {
		header.Set("Authorization", "Bearer "+tok)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}
	conn, resp, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial %s 失败: %w (HTTP %d)", url, err, resp.StatusCode)
		}
		return fmt.Errorf("dial %s 失败: %w", url, err)
	}
	defer conn.Close()
	zaplog.Logger.Infof("WebSocket 已连接: %s", url)

	// ctx 取消时主动关闭连接，让 ReadMessage 立刻返回
	closeOnCancel := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closeOnCancel:
		}
	}()
	defer close(closeOnCancel)

	// 收到一条事件后调 SendGroupAtText 等动作时复用这个 client（连接池友好）。
	// 注意每条 ExecCmd 自己又会 NewClientPool 一份——这是历史遗留，无大碍。
	httpClient := client_pool.NewClientPool()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read 失败: %w", err)
		}
		handleEvent(httpClient, raw)
	}
}

// eventEnvelope 只解出 post_type / message_type，用来 dispatch；
// 详细字段二次反序列化到 model.Message。
type eventEnvelope struct {
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type"`
}

func handleEvent(client *http.Client, raw []byte) {
	var hdr eventEnvelope
	if err := json.Unmarshal(raw, &hdr); err != nil {
		zaplog.Logger.Debugf("WebSocket 事件 JSON 头解析失败: %v, raw=%s", err, truncate(string(raw), 256))
		return
	}

	// 我们只处理群消息事件。
	// 心跳 / API 响应 / notice / private message / 其它 OneBot 事件全部忽略。
	if hdr.PostType != "message" || hdr.MessageType != "group" {
		zaplog.Logger.Debugf("WebSocket 事件忽略: post_type=%s message_type=%s", hdr.PostType, hdr.MessageType)
		return
	}

	var msg model.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		zaplog.Logger.Errorf("WebSocket 群消息反序列化失败: %v, raw=%s", err, truncate(string(raw), 256))
		return
	}
	logic.HandleAtMessage(client, &msg)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
