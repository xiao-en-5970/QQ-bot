// context_cache.go —— Moonshot 专属 Context Cache 接入（仅当主 BaseURL 指向 Moonshot 时启用）。
//
// 核心思想：长 system prompt 在 Moonshot 服务端注册成命名缓存，后续请求只塞
// cache 引用 + reset_ttl 续命；命中按"缓存 token 单价"计费（≈ 普通输入的 1/5）。
//
// ## 重构后的多平台行为（2026 年 6 月）
//
// bot 主链路改用 OpenAI 兼容协议（go-openai SDK）后，本模块的行为按平台分流：
//
// | 平台 | 显式 /v1/caching API | 行为 |
// |------|----------------------|------|
// | Moonshot/Kimi | ✅ 仅 moonshot-v1 family | 走 raw HTTP 调 /v1/caching 注册 cache |
// | Moonshot K2 系列 | ❌ 不支持 | 服务端自动前缀缓存（cacheEntry 不 prime） |
// | DeepSeek / OpenAI / Qwen 等 | ❌ 都不支持 | 各自有自动前缀缓存（cacheEntry 不 prime） |
//
// 所以在非 Moonshot 平台或非 v1 family 上，cacheEntry 字段保持空，loadID() 返回 ""，
// 所有调用走"每次发完整 system prompt"老路——服务端自动前缀缓存仍能命中相同前缀，
// **token 计费保持优惠**，只是 bot 端无需也无法显式管理 cache_id。
//
// ## 本项目的接入策略
//
// 默认 Model = `moonshot-v1-auto`、RecognizeModel = `kimi-k2-0905-preview`：
//
//   - **recognize / ops_sql**（K2 系列）：cacheEntry 字段空，不主动 prime；
//     bot 每次发 system message 字节级相同，让 K2 服务端自动前缀缓存命中。
//   - **chat**（moonshot-v1-auto）：仅当 IsMoonshotPlatform() && prompt 长度 ≥ 1000 时启用
//     显式 cache；否则走老路。
//
// ## 设计要点
//
//   - 启动时**异步并行** prime 启用的 cache（不阻塞 InitKimi 返回）
//   - 创建失败 / cache 失效 → 该路径自动 fallback 到原"每次发 system prompt"模式
//     业务零影响；失效后后台会重建缓存
//   - 每次请求 reset_ttl 续命；进程一直跑就一直续，过期不需要主动操心
//   - 进程重启会丢内存 cache_id 但创建一次后用很多次本身就回本，不持久化

package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qq_bot/conf"
	zaplog "qq_bot/utils/zap"

	openai "github.com/sashabaranov/go-openai"
)

const (
	// contextCacheTTL 缓存初始有效期。Moonshot 计费按"命中次数 + 缓存 token 数"双因素，
	// TTL 长一点不会增加费用——每次请求都顺手 reset_ttl=同值 续命。
	contextCacheTTL = 7 * 24 * time.Hour

	// contextCacheResetTTLSeconds 每次请求 reset 的秒数；跟初始 TTL 一致让 cache 跟着
	// bot 进程的活跃度自然续命。
	contextCacheResetTTLSeconds = int64(7 * 24 * 60 * 60)

	// contextCachePrimeTimeout 单次 cache 创建超时。Moonshot 计算 cache token 时偶尔慢。
	contextCachePrimeTimeout = 30 * time.Second

	// chatPromptCacheThreshold chat system prompt 至少多长才尝试创建缓存。
	//
	// Moonshot 文档未公开"最低 token 数"门槛，但实测短 prompt（<1000 chars / ~500 tokens）
	// 创建经常报错"prompt too short for caching"。所以 chat 这边按 prompt 字符数预筛——
	// 默认 conf 兜底的 prompt（~50 字）会被跳过，用户自定义长 prompt 时才会进缓存路径。
	chatPromptCacheThreshold = 1000 // chars
)

// cacheEntry 一份独立 prompt 缓存的状态机。
//
// 多个独立用途（recognize / ops_sql / chat）各拥有自己的 cacheEntry，互不影响。
// 非 Moonshot 平台所有字段保持空——loadID() 返回 ""，dropIfMissing 也 noop。
type cacheEntry struct {
	// 创建时确定，运行期不再改：
	name         string
	systemPrompt string
	// 运行期可变：
	id        atomic.Value // string；空 = 未就绪 / 创建失败 / 主动失效
	primeOnce sync.Once
}

func (e *cacheEntry) loadID() string {
	if e == nil {
		return ""
	}
	v, _ := e.id.Load().(string)
	return v
}

func (e *cacheEntry) storeID(id string) {
	e.id.Store(id)
}

// prime 同步创建一份 Moonshot 缓存，结果写入 e.id。失败仅 warn。
//
// 仅当主 BaseURL 指向 Moonshot 时才真正调 /v1/caching；其它平台 / 空 systemPrompt 直接 return。
func (e *cacheEntry) prime(parent context.Context, _ *openai.Client) {
	if e.systemPrompt == "" {
		return
	}
	if !conf.Cfg.Gpt.IsMoonshotPlatform() {
		// 非 Moonshot 平台不支持显式 cache，直接退出，依靠各家自动前缀缓存
		return
	}
	ctx, cancel := context.WithTimeout(parent, contextCachePrimeTimeout)
	defer cancel()

	id, tokens, err := moonshotCreateCache(ctx, e.name, e.systemPrompt)
	if err != nil {
		zaplog.Logger.Warnf("Moonshot context cache 创建失败 name=%s, 该路径将退化到每次发完整 system prompt: %v",
			e.name, err)
		return
	}
	if id == "" {
		zaplog.Logger.Warnf("Moonshot context cache name=%s 创建返回空 id; 该路径走老路", e.name)
		return
	}
	e.storeID(id)
	zaplog.Logger.Infof("Moonshot context cache 就绪 name=%s id=%s tokens=%d ttl=%s",
		e.name, id, tokens, contextCacheTTL)
}

// startPrimeOnce 启动时调一次（非阻塞）：异步创建缓存。
func (e *cacheEntry) startPrimeOnce(parent context.Context, cli *openai.Client) {
	e.primeOnce.Do(func() {
		go e.prime(parent, cli)
	})
}

// dropIfMissing 把"cache not found"类错误识别出来：清空内存 id 并触发后台重建。
//
// 没有统一的"cache 不存在"错误码；按响应里 message 的关键字判定。保守起见——
// 错杀的代价只是多发一次 system prompt 老路。
//
// **非 Moonshot 平台永远返回 false**（我们根本没设过 cache，错误肯定不是 cache 失效）。
func (e *cacheEntry) dropIfMissing(err error, cli *openai.Client) bool {
	if err == nil {
		return false
	}
	if !conf.Cfg.Gpt.IsMoonshotPlatform() {
		return false
	}
	msg := strings.ToLower(err.Error())
	hits := []string{
		"cache not found",
		"context cache not found",
		"invalid cache",
		"cache_id",
	}
	hit := false
	for _, h := range hits {
		if strings.Contains(msg, h) {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	zaplog.Logger.Warnf("Moonshot context cache 失效 name=%s, 清空 id 并触发后台重建: %v", e.name, err)
	e.storeID("")
	// 后台重建：用不会被本次 ctx cancel 影响的新 context；只重置 once 是为了让外部
	// 看到的 startPrimeOnce 仍然幂等，这里直接绕过 once 触发重建。
	go e.prime(context.Background(), cli)
	return true
}

// cacheReferenceMessage 构造一条 "Role:cache, Content:cache_id=...;reset_ttl=..." 的
// 消息——所有调用点共用同一格式。
//
// 注：openai.ChatCompletionMessage 没有 RoleContextCache 这个值（这是 Moonshot 私有
// 扩展），但 Role 字段是 string 类型，可以直接写 "cache" —— Moonshot 服务端能正确解读。
// 非 Moonshot 平台 cacheID 永远是空，所以不会构造这种消息，调用方无需担心兼容性。
func cacheReferenceMessage(id string) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{
		Role:    "cache",
		Content: fmt.Sprintf("cache_id=%s;reset_ttl=%d", id, contextCacheResetTTLSeconds),
	}
}

// StartContextCachePrime 启动时调一次（非阻塞）：异步并行 prime 所有启用的 cache。
//
// recognize / ops_sql 的 cacheEntry 字段为空（systemPrompt == ""），prime 内部
// 直接 return；只有 chat（且 prompt 足够长 + 平台是 Moonshot）会真正调 API 创建缓存。
//
// 调用方应在 InitKimi 之后调。失败仅 warn，业务零影响（各调用路径自动 fallback
// 到每次发完整 system prompt）。
func (k *Kimi) StartContextCachePrime(parent context.Context) {
	if k == nil {
		return
	}
	if !conf.Cfg.Gpt.IsMoonshotPlatform() {
		zaplog.Logger.Infof("context cache: 非 Moonshot 平台（base_url=%s），跳过显式 cache 注册，依赖各平台自动前缀缓存",
			conf.Cfg.Gpt.EffectiveBaseURL())
		return
	}
	zaplog.Logger.Infof("context cache: recognize / ops_sql 走 Moonshot K2 自动前缀缓存（每次请求 system 不变即可命中）")

	// chatCache 只在 prompt 足够长 + Moonshot 平台时被 InitKimi 填字段——空字段 systemPrompt
	// 的 prime 内部会直接 return 不调 API，所以无脑 start 也安全。
	k.recognizeCache.startPrimeOnce(parent, k.cli)
	k.opsSQLCache.startPrimeOnce(parent, k.cli)
	k.chatCache.startPrimeOnce(parent, k.cli)
}

// ============================================================================
// Moonshot /v1/caching raw HTTP 客户端（go-openai SDK 不支持这个专属接口）
// ============================================================================

// moonshotCreateCacheReq /v1/caching POST 请求体。
type moonshotCreateCacheReq struct {
	Model     string                 `json:"model"`
	Messages  []map[string]string    `json:"messages"`
	Name      string                 `json:"name,omitempty"`
	TTL       int64                  `json:"ttl,omitempty"`
	ExpiredAt int64                  `json:"expired_at,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// moonshotCreateCacheResp /v1/caching POST 响应体（仅取需要的字段）。
type moonshotCreateCacheResp struct {
	ID     string `json:"id"`
	Tokens int    `json:"tokens"`
}

// moonshotCreateCache 通过 raw HTTP 调 Moonshot /v1/caching 接口创建一个命名缓存。
//
// 跟 go-openai SDK 解耦——caching 是 Moonshot 私有扩展，没有标准 API 定义。
// 只在 conf.Cfg.Gpt.IsMoonshotPlatform() 为 true 时才被调到。
func moonshotCreateCache(ctx context.Context, name, systemPrompt string) (string, int, error) {
	reqBody := moonshotCreateCacheReq{
		// 走 moonshot-v1 family（默认 Model 通常就是 moonshot-v1-auto；K2 family 不支持
		// caching，由调用方在 IsMoonshotPlatform 后还需要保证 prompt + Model 属于 v1）。
		Model: "moonshot-v1",
		Messages: []map[string]string{
			{"role": "system", "content": systemPrompt},
		},
		Name:      name,
		TTL:       int64(contextCacheTTL.Seconds()),
		ExpiredAt: time.Now().Add(contextCacheTTL).Unix(),
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("marshal cache req: %w", err)
	}

	url := strings.TrimRight(conf.Cfg.Gpt.EffectiveBaseURL(), "/") + "/caching"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", 0, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+conf.Cfg.Gpt.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateLog(string(body), 400))
	}
	var out moonshotCreateCacheResp
	if err := json.Unmarshal(body, &out); err != nil {
		return "", 0, fmt.Errorf("unmarshal resp: %w, raw=%s", err, truncateLog(string(body), 200))
	}
	return out.ID, out.Tokens, nil
}
