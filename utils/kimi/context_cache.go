// context_cache.go —— Moonshot Context Cache 接入。
//
// 核心思想：长 system prompt 注册成命名缓存，后续请求只塞 cache 引用 + reset_ttl
// 续命；命中按"缓存 token 单价"计费（≈ 普通输入的 1/5）。
//
// 当前接入的 3 个 cache：
//
//   - recognize  业务动作识别（recognize.go）—— prompt ≈ 5000+ tokens
//   - ops_sql    运维群"自然语言转 SQL"（ops_sql.go）—— prompt ≈ 3000 tokens
//   - chat       @bot 聊天（kimi.go）—— prompt 长度由用户配 GPT_SYSTEM_PROMPT
//                决定；conf.applyDefaults 兜底很短（~50 tokens），低于 Moonshot 缓存
//                最低门槛会创建失败——见 chatPromptCacheThreshold 注释
//
// 不接入的：
//
//   - ops_summary  运维结果总结 prompt < 200 tokens（达不到缓存最低门槛）
//   - vision OCR   单图调用、prompt 短（~500 tokens），且图片本身才是大头
//
// 设计要点：
//   - 启动时**异步并行** prime 各 cache（不阻塞 InitKimi 返回）
//   - 创建失败 / cache 失效 → 该路径自动 fallback 到原"每次发 system prompt"模式
//     业务零影响；后台会重建缓存
//   - 每次请求 reset_ttl 续命；进程一直跑就一直续，过期不需要主动操心
//   - 进程重启会丢内存 cache_id 但创建一次后用很多次本身就回本，不持久化

package kimi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	zaplog "qq_bot/utils/zap"

	"github.com/northes/go-moonshot"
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
// 多个独立用途（recognize / ops_sql / chat）各拥有自己的 cacheEntry，互不影响：
//   - 某一份 prime 失败不会拖累其它路径
//   - 不同 model family 各自缓存（Moonshot 按 family 区分缓存）
type cacheEntry struct {
	// 创建时确定，运行期不再改：
	name         string
	modelFamily  moonshot.ChatCompletionsModelFamily
	systemPrompt string
	// 运行期可变：
	id        atomic.Value // string；空 = 未就绪 / 创建失败 / 主动失效
	primeOnce sync.Once
}

func (e *cacheEntry) loadID() string {
	v, _ := e.id.Load().(string)
	return v
}

func (e *cacheEntry) storeID(id string) {
	e.id.Store(id)
}

// prime 同步创建一份缓存，结果写入 e.id。失败仅 warn。
func (e *cacheEntry) prime(parent context.Context, cli *moonshot.Client) {
	if e.systemPrompt == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, contextCachePrimeTimeout)
	defer cancel()

	req := &moonshot.ContextCacheCreateRequest{
		Model: e.modelFamily,
		Messages: []moonshot.ChatCompletionsMessage{
			{Role: moonshot.RoleSystem, Content: e.systemPrompt},
		},
		Name:      e.name,
		TTL:       int64(contextCacheTTL.Seconds()),
		ExpiredAt: time.Now().Add(contextCacheTTL).Unix(),
	}
	resp, err := cli.ContextCache().Create(ctx, req)
	if err != nil {
		zaplog.Logger.Warnf("context cache 创建失败 name=%s family=%s, 该路径将退化到每次发完整 system prompt: %v",
			e.name, e.modelFamily, err)
		return
	}
	if resp == nil || resp.Id == "" {
		zaplog.Logger.Warnf("context cache name=%s 创建返回空 id; 该路径走老路", e.name)
		return
	}
	e.storeID(resp.Id)
	zaplog.Logger.Infof("context cache 就绪 name=%s id=%s tokens=%d ttl=%s",
		e.name, resp.Id, resp.Tokens, contextCacheTTL)
}

// startPrimeOnce 启动时调一次（非阻塞）：异步创建缓存。
func (e *cacheEntry) startPrimeOnce(parent context.Context, cli *moonshot.Client) {
	e.primeOnce.Do(func() {
		go e.prime(parent, cli)
	})
}

// dropIfMissing 把"cache not found"类错误识别出来：清空内存 id 并触发后台重建。
//
// Moonshot 没有统一的"cache 不存在"错误码；按响应里 message 的关键字判定。保守起见——
// 错杀的代价只是多发一次 system prompt 老路。
func (e *cacheEntry) dropIfMissing(err error, cli *moonshot.Client) bool {
	if err == nil {
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
	zaplog.Logger.Warnf("context cache 失效 name=%s, 清空 id 并触发后台重建: %v", e.name, err)
	e.storeID("")
	// 后台重建：用不会被本次 ctx cancel 影响的新 context；只重置 once 是为了让外部
	// 看到的 startPrimeOnce 仍然幂等，这里直接绕过 once 触发重建。
	go e.prime(context.Background(), cli)
	return true
}

// cacheReferenceMessage 构造一条 "Role:cache, Content:cache_id=...;reset_ttl=..." 的
// 消息——所有调用点共用同一格式。
func cacheReferenceMessage(id string) *moonshot.ChatCompletionsMessage {
	return &moonshot.ChatCompletionsMessage{
		Role:    moonshot.RoleContextCache,
		Content: fmt.Sprintf("cache_id=%s;reset_ttl=%d", id, contextCacheResetTTLSeconds),
	}
}

// =============================================================================
// 三份缓存实例的 model family 选择
//
// Moonshot Context Cache 按 model family 缓存——同一 family 内不同 variant
// （8k/32k/128k）共用同一份缓存。无需匹配具体 variant。
// =============================================================================

// kimiK2Family Kimi K2 系列（kimi-k2-0905-preview 等）所属的 family 字符串。
// SDK 没有 enum 常量，由我们按 Moonshot 文档命名传字符串。
const kimiK2Family moonshot.ChatCompletionsModelFamily = "kimi-k2"

// StartContextCachePrime 启动时调一次（非阻塞）：异步并行 prime 所有 cache。
//
// 调用方应在 InitKimi 之后调。失败仅 warn，业务零影响（各调用路径自动 fallback
// 到每次发完整 system prompt）。
func (k *Kimi) StartContextCachePrime(parent context.Context) {
	if k == nil {
		return
	}
	k.recognizeCache.startPrimeOnce(parent, k.cli)
	k.opsSQLCache.startPrimeOnce(parent, k.cli)
	// chatCache 只在 prompt 足够长时才被 InitKimi 填字段——空字段时 prime 内部会
	// 直接 return 不调 Moonshot，所以这里无脑 start 也安全
	k.chatCache.startPrimeOnce(parent, k.cli)
}
