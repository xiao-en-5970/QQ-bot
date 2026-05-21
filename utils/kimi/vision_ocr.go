// vision_ocr.go —— "纯图片上架" 通道：当用户在群里只发了几张图、没补任何业务文字
// 时，对每张图单独调一次 Moonshot vision API，让模型直接从图片里 OCR + 抽取
// title / price / category 等并返回一条 RecognizeAction，dispatch 端按"图文上架"
// 同样的链路写库。
//
// 跟 RecognizeBusinessActions 的区别：
//   - 后者把窗口快照（含 [图片] 占位符）的纯文本 JSON 喂给文本模型 K2，
//     不把图片真的传过去
//   - 本函数把**单张图片 URL** 通过 OpenAI 风格 vision content array 传给视觉模型，
//     让模型直接看图，每张图独立产出一条 action
//
// 不复用现有 *moonshot.Client：SDK 的 ChatCompletionsMessage.Content 只支持 string，
// vision API 要求 content 是数组（type=image_url + type=text）。直接绕过 SDK 走
// 原生 http.Client 发请求；只为这一个端点，代码量也少。

package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"qq_bot/conf"
	zaplog "qq_bot/utils/zap"
	"time"
)

// moonshotVisionEndpoint 与 SDK 内部一致：官方域名 + /v1/chat/completions
const moonshotVisionEndpoint = "https://api.moonshot.cn/v1/chat/completions"

// visionHTTPTimeout 单次 vision 调用超时（含模型推理 + 网络）。
//
// 视觉调用比纯文本慢一些；30s 留出余量但避免 fail-fast 缺失。
const visionHTTPTimeout = 30 * time.Second

// visionUserPrompt 告诉模型"看这张图、抽出商品信息按既定 JSON 输出"。
//
// 复用 recognizeSystemPrompt 是不行的：那个系统提示假设输入是窗口快照 JSON、要
// 处理多 action / 图片归属 / 时间窗等概念。OCR 单图场景简化很多——直接告诉模型
// 输出单条 action，且只用 publish_good 这一种类型。
const visionUserPrompt = `这张图是 QQ 群里某位用户单独发出的图片，没有任何配文。
请直接从图里识别商品信息（OCR 文字 + 视觉理解），按下面规则输出**严格 JSON**：

{"type": "publish_good" | "none",
 "confidence": 0.0~1.0,
 "reason": "一句话说明判定理由",
 "title": "...",        // 商品名（短，不含数量/价格）
 "price": 数字,          // 单位:元；没有价格信息时省略此字段
 "negotiable": bool,    // 图里写"面议"则 true；其它情况 false
 "category": 1,         // 固定 1=二手
 "stock": N             // 数量；图里没说则省略
}

判定规则（**保守优先**）：

1. **必须**图里能 OCR 出**商品名 + (价格 或 "已开封/剩余/全新" 等明确二手卖出关键词)**才算 publish_good
2. 不能可靠看出商品名 / 没有价格也没有任何卖出关键词 → 输出 {"type":"none","confidence":...,"reason":"..."}
3. 价格写"3元" / "5r" / "10块" / "￥10" → price=数字（单位元）
4. 价格写"白送" / "免费" / "0元" → price=0, negotiable=false
5. 写"面议" / "私聊价" → 不输出 price, negotiable=true
6. title 用简洁的商品名（如"洗手液"、"除螨喷雾"），不要带"还剩一半"、"只用了两次"等状态描述——
   这些放进 reason 里说明就够，不要污染 title
7. 输出**必须**只有一个 JSON 对象，**不要**包 markdown 也不要写解释`

// visionContent 单条 content 段（vision 模型的 message.content 是这样的数组）。
type visionContent struct {
	Type     string                `json:"type"` // "image_url" 或 "text"
	ImageURL *visionImageURLDetail `json:"image_url,omitempty"`
	Text     string                `json:"text,omitempty"`
}

type visionImageURLDetail struct {
	URL string `json:"url"` // 公网可访问 URL；NapCat 临时图片 URL 也能用
}

type visionMessage struct {
	Role    string          `json:"role"`
	Content []visionContent `json:"content"`
}

type visionRequest struct {
	Model          string                            `json:"model"`
	Messages       []visionMessage                   `json:"messages"`
	Temperature    float64                           `json:"temperature"`
	ResponseFormat *visionResponseFormat             `json:"response_format,omitempty"`
}

type visionResponseFormat struct {
	Type string `json:"type"` // "json_object"
}

type visionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// RecognizeFromImage 对单张图片做 OCR + 商品信息抽取。
//
// 失败模式：
//   - ErrQuotaCooling：识别配额冷却期；上层应当静默丢弃（与 RecognizeBusinessActions 同语义）
//   - 其它 error：网络 / 解析失败；上层 log 后跳过本张图（继续尝试下一张）
//
// 返回的 action 已经填好 SourceMessageIDs / ImageMessageIDs 字段——上层不用再加。
//
// imageURL 必须是公网 HTTPS / HTTP；NapCat 的 message segment 里的图片 URL 直接能用。
// messageID 是图片所在那条群消息的外层 message_id，让 dispatch 端能正确写到
// goods.bot_message_ids（让后续 reply 反查命中）。
func (k *Kimi) RecognizeFromImage(ctx context.Context, imageURL string, messageID int64) (*RecognizeAction, error) {
	if k == nil {
		return nil, errors.New("kimi 未启用")
	}
	if imageURL == "" {
		return nil, errors.New("imageURL 为空")
	}
	if blocked, _ := globalQuotaGate.IsBlocked(); blocked {
		return nil, ErrQuotaCooling
	}

	reqBody := visionRequest{
		Model: conf.Cfg.Gpt.VisionModel,
		Messages: []visionMessage{
			{
				Role: "user",
				Content: []visionContent{
					{Type: "image_url", ImageURL: &visionImageURLDetail{URL: imageURL}},
					{Type: "text", Text: visionUserPrompt},
				},
			},
		},
		Temperature:    0.2,
		ResponseFormat: &visionResponseFormat{Type: "json_object"},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("vision req marshal: %w", err)
	}

	// 用独立 http.Client 而非 SDK：vision API 的 content 是数组，SDK 类型只支持
	// string content 不兼容。
	httpClient := &http.Client{Timeout: visionHTTPTimeout}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, moonshotVisionEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("vision new req: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+conf.Cfg.Gpt.APIKey)

	httpResp, err := httpClient.Do(httpReq)
	globalQuotaGate.RecordResult(err)
	if err != nil {
		return nil, fmt.Errorf("vision http do: %w", err)
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("vision read body: %w", err)
	}
	if httpResp.StatusCode/100 != 2 {
		// 把错误 body 截短带出去——quota 错 / 模型限流等需要在 log 里能直接看到原因
		return nil, fmt.Errorf("vision HTTP %d: %s", httpResp.StatusCode, truncateRaw(string(raw), 200))
	}

	var resp visionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("vision resp unmarshal: %w, raw=%s", err, truncateRaw(string(raw), 200))
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("vision biz error: %s/%s/%s", resp.Error.Type, resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("vision 返回 choices 为空")
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		return nil, errors.New("vision 返回 content 为空")
	}
	zaplog.Logger.Debugf("vision raw content msg=%d: %s", messageID, truncateRaw(content, 200))

	var action RecognizeAction
	if err := json.Unmarshal([]byte(content), &action); err != nil {
		return nil, fmt.Errorf("vision JSON 解析失败: %w, raw=%s", err, truncateRaw(content, 200))
	}
	// 强制 category=1（视觉 OCR 只走二手卖出场景，求物品需要文字背景才能判定）
	action.Category = 1
	// 关联 message_id 让 dispatch 端写到 goods.bot_message_ids，reply 反查就能命中
	if messageID != 0 {
		action.ImageMessageIDs = []int64{messageID}
		action.SourceMessageIDs = []int64{messageID}
	}
	return &action, nil
}

func truncateRaw(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
