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
// 关键：**不直接把 NapCat URL 给 Moonshot**——NapCat 的临时图片 URL 形如
// https://multimedia.nt.qq.com.cn/download?...&rkey=...，rkey 是 QQ 客户端
// 鉴权 token，只对发起请求的客户端 IP / 设备有效。Moonshot 服务端从它自己的
// 机房 IP 去拉时 QQ 会拒绝（403/404）。所以本函数 **先 bot 端下载图片 → base64
// 编码 → 以 data: URI 形式传给 Moonshot**，绕开跨服务器鉴权问题。
//
// 不复用现有 *moonshot.Client：SDK 的 ChatCompletionsMessage.Content 只支持 string，
// vision API 要求 content 是数组（type=image_url + type=text）。直接绕过 SDK 走
// 原生 http.Client 发请求；只为这一个端点，代码量也少。

package kimi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"qq_bot/conf"
	zaplog "qq_bot/utils/zap"
	"strings"
	"time"
)

// visionEndpointPath 视觉调用使用的 chat completions 路径。
// 实际 URL = conf.Cfg.Gpt.EffectiveVisionBaseURL() + 这段。
// 默认指向 Moonshot；切到 DeepSeek 主链路时可独立配置 GPT_VISION_BASE_URL=
// https://api.moonshot.cn/v1 保持 vision 走 Moonshot。
const visionEndpointPath = "/chat/completions"

// visionHTTPTimeout 单次 vision 调用超时（含模型推理 + 网络）。
//
// 视觉调用比纯文本慢一些；30s 留出余量但避免 fail-fast 缺失。
const visionHTTPTimeout = 30 * time.Second

// visionDownloadTimeout 下载 NapCat 图片到本地的超时。
const visionDownloadTimeout = 15 * time.Second

// visionMaxImageBytes Moonshot vision API 单张图上限（实测 ~10MB）；超大图我们
// 直接拒绝，避免 base64 后超过 10MB 触发 Moonshot 413。
const visionMaxImageBytes = 8 * 1024 * 1024

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

	// 第 0 步：把 NapCat 鉴权 URL 下载到本地转 base64 data URI。
	// Moonshot 服务端拉不到 NapCat 临时 URL（rkey 只对发起客户端有效）；
	// 下载完成后用 data:image/...;base64,... 形式给 Moonshot，绕开跨服务器鉴权。
	dataURI, err := downloadImageAsDataURI(ctx, imageURL)
	if err != nil {
		return nil, fmt.Errorf("vision download image: %w", err)
	}
	zaplog.Logger.Infof("vision OCR msg=%d 图片已下载 size=%dKB", messageID, len(dataURI)*3/4/1024)

	reqBody := visionRequest{
		Model: conf.Cfg.Gpt.VisionModel,
		Messages: []visionMessage{
			{
				Role: "user",
				Content: []visionContent{
					{Type: "image_url", ImageURL: &visionImageURLDetail{URL: dataURI}},
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
	endpoint := strings.TrimRight(conf.Cfg.Gpt.EffectiveVisionBaseURL(), "/") + visionEndpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("vision new req: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+conf.Cfg.Gpt.EffectiveVisionAPIKey())

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
	zaplog.Logger.Infof("vision raw content msg=%d: %s", messageID, truncateRaw(content, 400))

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

// downloadImageAsDataURI 把 NapCat 临时 URL 的图片下载下来，转成
// data:image/<mime>;base64,<...> 形式返回。Moonshot vision API 接受 data: URI
// 的 image_url，不需要再去自己拉远程图片。
//
// 兼容 NapCat URL 里可能出现的 &amp; HTML 实体（个别版本 raw 字段写到 segments
// 里时没做反转义；正常 segments.url 不会出现，但加一道兜底）。
func downloadImageAsDataURI(parent context.Context, srcURL string) (string, error) {
	srcURL = strings.ReplaceAll(srcURL, "&amp;", "&")

	dlCtx, cancel := context.WithTimeout(parent, visionDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, srcURL, nil)
	if err != nil {
		return "", fmt.Errorf("new req: %w", err)
	}
	// NapCat 给的腾讯多媒体域名对 UA 不挑，但带一个常规 UA 更安全
	req.Header.Set("User-Agent", "qq-bot-vision/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("HTTP %d 下载图片失败 url=%s", resp.StatusCode, truncateRaw(srcURL, 120))
	}

	// 限流读取，防止超大图把内存撑爆。
	lr := io.LimitReader(resp.Body, visionMaxImageBytes+1)
	raw, err := io.ReadAll(lr)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if len(raw) > visionMaxImageBytes {
		return "", fmt.Errorf("图片过大（>%dMB），跳过", visionMaxImageBytes/1024/1024)
	}
	if len(raw) == 0 {
		return "", errors.New("图片下载到 0 字节")
	}

	mime := detectImageMIME(raw)
	if mime == "" {
		mime = "image/jpeg" // 默认 jpeg，QQ 图片绝大多数是 jpeg
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	return "data:" + mime + ";base64," + encoded, nil
}

// detectImageMIME 用 magic bytes 判 MIME；只覆盖 QQ 群里能见到的主流格式。
//
// Moonshot vision 文档支持 image/jpeg、image/png、image/gif、image/webp 这几种。
func detectImageMIME(raw []byte) string {
	if len(raw) < 4 {
		return ""
	}
	switch {
	case bytes.HasPrefix(raw, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(raw, []byte{0x89, 0x50, 0x4E, 0x47}):
		return "image/png"
	case bytes.HasPrefix(raw, []byte("GIF8")):
		return "image/gif"
	case len(raw) >= 12 && bytes.Equal(raw[0:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")):
		return "image/webp"
	case bytes.HasPrefix(raw, []byte{0x42, 0x4D}):
		return "image/bmp"
	}
	return ""
}
