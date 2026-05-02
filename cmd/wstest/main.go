// wstest 是一次性 WebSocket 端到端验证工具。
//
// 用途：
//
//	跑一次，对 NapCat 的 wss 端点完整地走一遍：dial → 鉴权 → 发 send_group_msg → 读事件。
//	能跑通就证明 token 对、反代 Upgrade/超时配置对、上下行都通。
//	不依赖 conf/test.yaml；所有参数都写死在下面的 const 区，方便手改。
//
// 用法：
//
//	cd QQ-bot
//	go run ./cmd/wstest
//
// 跑完会在标准输出打印连接握手结果、上行 echo 回包、最多 5 秒内收到的所有 push 事件。
// 同时群 1027373936 应该会看到 bot 发出去的那条测试文本。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsURL       = "wss://bot-ws.xiaoen.xyz/"
	accessToken = "ws-theo5970"
	testGroupID = 1027373936
	testText    = "WebSocket 双向联通测试 - 如果群里看到这条说明上行 OK"
	listenFor   = 5 * time.Second
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// === 1. 拨号 + 鉴权 =====================================================
	header := http.Header{}
	if accessToken != "" {
		header.Set("Authorization", "Bearer "+accessToken)
	}

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	fmt.Printf("[wstest] dialing %s ...\n", wsURL)
	conn, resp, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			fmt.Fprintf(os.Stderr, "[wstest] dial 失败: %v (HTTP %d)\n", err, resp.StatusCode)
		} else {
			fmt.Fprintf(os.Stderr, "[wstest] dial 失败: %v\n", err)
		}
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Println("[wstest] WebSocket 握手成功 ✓")

	// === 2. 上行：发一条 send_group_msg =====================================
	// OneBot11 over WS：客户端发 {action, params, echo} 给服务端，服务端处理后用同 echo 回包。
	echoID := fmt.Sprintf("wstest-%d", time.Now().UnixNano())
	action := map[string]any{
		"action": "send_group_msg",
		"params": map[string]any{
			"group_id": testGroupID,
			"message": []map[string]any{
				{"type": "text", "data": map[string]string{"text": testText}},
			},
		},
		"echo": echoID,
	}
	payload, _ := json.Marshal(action)
	fmt.Printf("[wstest] -> %s\n", string(payload))
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		fmt.Fprintf(os.Stderr, "[wstest] write 失败: %v\n", err)
		os.Exit(1)
	}

	// === 3. 下行：读 5 秒，把所有事件 / 回包打印出来 ==========================
	deadline := time.Now().Add(listenFor)
	fmt.Printf("[wstest] 监听后续事件 %s ...\n", listenFor)
	gotEcho := false
	count := 0
	for {
		// 用 SetReadDeadline 限定剩余时间，到点 ReadMessage 就返回 timeout
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				break
			}
			fmt.Fprintf(os.Stderr, "[wstest] read 失败: %v\n", err)
			break
		}
		count++
		// 单独识别一下 echo 回包，方便看到底有没有送达
		var probe struct {
			Echo string `json:"echo"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Echo == echoID {
			gotEcho = true
			fmt.Printf("[wstest] <- ECHO: %s\n", string(raw))
		} else {
			fmt.Printf("[wstest] <- EVT(%d): %s\n", count, string(raw))
		}
	}

	// === 4. 总结 ============================================================
	fmt.Println("[wstest] ----------- 总结 -----------")
	fmt.Printf("[wstest] 握手:        ✓\n")
	if gotEcho {
		fmt.Printf("[wstest] 上行 echo 回包: ✓ (NapCat 收到了 send_group_msg)\n")
	} else {
		fmt.Printf("[wstest] 上行 echo 回包: ✗ (5s 内没收到 echo=%s 的回包)\n", echoID)
	}
	if count > 0 {
		fmt.Printf("[wstest] 下行事件总数:  %d 条 (说明 WS 是活的，事件流能下来)\n", count)
	} else {
		fmt.Printf("[wstest] 下行事件总数:  0 (5s 内一条都没收到，可能反代缓冲住了或反代杀了空闲连接)\n")
	}
	fmt.Println("[wstest] 现在去群 1027373936 看看那条测试消息是否出现。")
}
