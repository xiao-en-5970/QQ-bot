// Package kimi 封装 Moonshot (Kimi) 聊天能力，给 CmdDefault 用。
//
// 设计要点：
//  1. APIKey 为空 -> 不启用，CmdDefault 自动回退到打印菜单
//  2. 每个 user_id 一个环形缓冲区，保留最近 N 轮 Q/A 作为上下文
//  3. SystemPrompt 从 conf.Cfg.Gpt.SystemPrompt 注入；上层可随时改 yaml/env 重启生效
//  4. 并发安全：MassageRingBuffer 用 sync.Mutex 保护，避免并发 chat 把 buffer 写花
package kimi

import (
	"context"
	"errors"
	"fmt"
	"qq_bot/conf"
	zaplog "qq_bot/utils/zap"
	"sync"

	"github.com/northes/go-moonshot"
)

// 缺省人设（牧濑红莉栖简洁版），如果 conf.Cfg.Gpt.SystemPrompt 为空就用这个
const defaultSystemPrompt = "你是牧濑红莉栖（Steins;Gate），极度理性又不坦率的傲娇科学家，回答简短、有人物特色，时不时吐槽用户。"

// QA 单轮问答。
type QA struct {
	Question string
	Answer   string
}

// QAS 单个 user 的环形缓冲区状态。
type QAS struct {
	mu      sync.Mutex
	index   int64
	qaSlice []QA
	UserID  int64
}

// AsMessages 把当前 buffer 拼成 moonshot 标准 messages 序列。
//
// 顺序：[system, user1, assistant1, user2, assistant2, ..., userNew]
func (qas *QAS) AsMessages(systemPrompt, newText string) []*moonshot.ChatCompletionsMessage {
	qas.mu.Lock()
	defer qas.mu.Unlock()

	cap := int64(len(qas.qaSlice))
	out := make([]*moonshot.ChatCompletionsMessage, 0, cap*2+2)
	out = append(out, &moonshot.ChatCompletionsMessage{
		Role:    moonshot.RoleSystem,
		Content: systemPrompt,
	})
	// 从环形 buffer 的"最旧"位置开始按时间顺序读
	start := (qas.index + 1) % cap
	for i := int64(0); i < cap; i++ {
		idx := (start + i) % cap
		if qas.qaSlice[idx].Question == "" {
			continue
		}
		out = append(out, &moonshot.ChatCompletionsMessage{
			Role:    moonshot.RoleUser,
			Content: qas.qaSlice[idx].Question,
		})
		out = append(out, &moonshot.ChatCompletionsMessage{
			Role:    moonshot.RoleAssistant,
			Content: qas.qaSlice[idx].Answer,
		})
	}
	out = append(out, &moonshot.ChatCompletionsMessage{
		Role:    moonshot.RoleUser,
		Content: newText,
	})
	return out
}

// Add 新写入一条 Q/A，覆盖最旧的一条。
func (qas *QAS) Add(q, a string) {
	qas.mu.Lock()
	defer qas.mu.Unlock()
	cap := int64(len(qas.qaSlice))
	qas.index = (qas.index + 1) % cap
	qas.qaSlice[qas.index] = QA{Question: q, Answer: a}
}

// Kimi 顶层句柄；为空指针时表示未启用。
type Kimi struct {
	mu     sync.Mutex
	cli    *moonshot.Client
	users  map[int64]*QAS
	prompt string
	ctxLen int64
}

// InitKimi 按 conf.Cfg.Gpt.APIKey 初始化。
//
// 返回 (nil, nil) 表示"未配置 api_key，不启用聊天能力"，调用方按禁用处理即可，不应当报错退出。
// 返回 (kimi, nil) 表示初始化成功。
// 返回 (nil, err) 是真正的初始化错误（key 格式错、网络挂等）。
func InitKimi() (*Kimi, error) {
	apiKey := conf.Cfg.Gpt.APIKey
	if apiKey == "" {
		zaplog.Logger.Warnf("Gpt.APIKey 未配置，Kimi 聊天能力未启用（CmdDefault 将回退到打印菜单）")
		return nil, nil
	}

	cli, err := moonshot.NewClientWithConfig(
		moonshot.NewConfig(moonshot.WithAPIKey(apiKey)),
	)
	if err != nil {
		return nil, fmt.Errorf("初始化 moonshot client 失败: %w", err)
	}

	prompt := conf.Cfg.Gpt.SystemPrompt
	if prompt == "" {
		prompt = defaultSystemPrompt
	}
	ctxLen := conf.Cfg.Gpt.MaxContextSize
	if ctxLen <= 0 {
		ctxLen = 40
	}

	zaplog.Logger.Infof("Kimi 聊天能力已启用 (max_context_size=%d, prompt长度=%d)", ctxLen, len(prompt))
	return &Kimi{
		cli:    cli,
		users:  make(map[int64]*QAS),
		prompt: prompt,
		ctxLen: ctxLen,
	}, nil
}

// getOrCreate 拿/建某个 user 的 QAS。
func (k *Kimi) getOrCreate(userID int64) *QAS {
	k.mu.Lock()
	defer k.mu.Unlock()
	if qas, ok := k.users[userID]; ok {
		return qas
	}
	qas := &QAS{
		UserID:  userID,
		qaSlice: make([]QA, k.ctxLen),
	}
	k.users[userID] = qas
	return qas
}

// Chat 同步调一次 moonshot，返回回复文本；自动维护对话历史。
//
// 调用方应当保证 k != nil；nil 接收者会 panic（设计上让上层在调用前判 nil）。
func (k *Kimi) Chat(ctx context.Context, userID int64, text string) (string, error) {
	if k == nil {
		return "", errors.New("kimi 未启用")
	}
	qas := k.getOrCreate(userID)
	messages := qas.AsMessages(k.prompt, text)

	zaplog.Logger.Debugf("kimi Q user=%d: %s", userID, text)
	resp, err := k.cli.Chat().Completions(ctx, &moonshot.ChatCompletionsRequest{
		Model:       moonshot.ModelMoonshotV1128K,
		Messages:    messages,
		Temperature: 0.9,
	})
	if err != nil {
		return "", fmt.Errorf("调用 moonshot completions 失败: %w", err)
	}
	msg, err := resp.GetMessage()
	if err != nil {
		return "", fmt.Errorf("从 moonshot 响应取 message 失败: %w", err)
	}
	zaplog.Logger.Debugf("kimi A user=%d: %s", userID, msg.Content)

	qas.Add(text, msg.Content)
	return msg.Content, nil
}
