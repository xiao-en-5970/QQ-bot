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
	"time"

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

	zaplog.Logger.Infof("Kimi 聊天能力已启用 (chat_model=%s, recognize_model=%s, max_context_size=%d, prompt长度=%d, quota_cooldown=%ds)",
		conf.Cfg.Gpt.Model, conf.Cfg.Gpt.RecognizeModel, ctxLen, len(prompt), conf.Cfg.Gpt.QuotaCooldownSeconds)
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

// Chat 同步调 moonshot 一轮或多轮（带 tool calling），返回最终的回复文本；自动维护对话历史。
//
// 协议（OpenAI 标准 function calling）：
//  1. 我们带 messages + tools 发请求；
//  2. Kimi 回 message。如果 finish_reason=tool_calls，里面有要调的工具及参数；
//  3. 我们本地跑 Handler 拿 result；
//  4. 把 assistant 的 tool_calls 消息 + 自己造的 role=tool 消息追加到 messages，
//     再发一次请求；
//  5. 直到 finish_reason=stop（或者超过 maxToolRounds 兜底退出）；
//  6. 最终的 assistant content 存进 QAS 历史（中间的 tool_calls / tool 消息不存，
//     用户视角看到的就是"我问→bot 答"，工具是 bot 内部细节）。
//
// 调用方应当保证 k != nil；nil 接收者会 panic（设计上让上层在调用前判 nil）。
func (k *Kimi) Chat(ctx context.Context, userID int64, text string) (string, error) {
	if k == nil {
		return "", errors.New("kimi 未启用")
	}
	// 进入 quota 冷却期则直接返回——闲聊不重要，宁可让用户感知到 bot 在喘息也不要继续撞 API
	if blocked, remain := globalQuotaGate.IsBlocked(); blocked {
		return "", fmt.Errorf("kimi quota 冷却中（剩余 %s），请稍后再试", remain.Truncate(time.Second))
	}
	qas := k.getOrCreate(userID)
	messages := qas.AsMessages(k.prompt, text)
	tools := toolSpecs()

	// 单次 Chat 里允许的工具往返轮次上限：防 Kimi 反复调工具不出最终答案。
	// 配置项是 conf.Cfg.Gpt.MaxToolRounds（env GPT_MAX_TOOL_ROUNDS），默认 5。
	maxRounds := conf.Cfg.Gpt.MaxToolRounds
	if maxRounds <= 0 {
		maxRounds = 5 // applyDefaults 应该已经填了，这里再兜一道防御
	}

	zaplog.Logger.Debugf("kimi Q user=%d: %s (tools=%d, max_rounds=%d)", userID, text, len(tools), maxRounds)

	chatModel := moonshot.ChatCompletionsModelID(conf.Cfg.Gpt.Model)
	for round := 0; round < maxRounds; round++ {
		resp, err := k.cli.Chat().Completions(ctx, &moonshot.ChatCompletionsRequest{
			Model:       chatModel,
			Messages:    messages,
			Temperature: 0.9,
			Tools:       tools, // nil 也 ok，moonshot 接 omitempty
		})
		globalQuotaGate.RecordResult(err)
		if err != nil {
			return "", fmt.Errorf("调用 moonshot completions 失败: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", errors.New("moonshot 返回 0 choices")
		}
		choice := resp.Choices[0]
		msg := choice.Message

		// 没有 tool_calls 或 finish_reason=stop -> 拿到最终回答，结束循环
		if len(msg.ToolCalls) == 0 || choice.FinishReason == moonshot.FinishReasonStop {
			zaplog.Logger.Debugf("kimi A user=%d round=%d: %s", userID, round, msg.Content)
			qas.Add(text, msg.Content)
			return msg.Content, nil
		}

		// 有 tool_calls -> 本地依次执行，把 assistant 消息 + 各 tool 结果消息追加进 messages
		zaplog.Logger.Infof("kimi user=%d round=%d 触发 %d 个 tool_calls", userID, round, len(msg.ToolCalls))
		messages = append(messages, msg)
		for _, call := range msg.ToolCalls {
			if call.Function == nil {
				continue
			}
			result := invokeTool(ctx, call.Function.Name, call.Function.Arguments)
			zaplog.Logger.Infof("kimi tool=%s args=%s -> %s",
				call.Function.Name, truncateLog(call.Function.Arguments, 200), truncateLog(result, 200))
			messages = append(messages, &moonshot.ChatCompletionsMessage{
				Role:       moonshot.RoleTool,
				Content:    result,
				ToolCallID: call.ID,
			})
		}
	}

	return "", fmt.Errorf("kimi 连续 %d 轮 tool_calls 仍未给最终答案，已放弃（保护性退出）", maxRounds)
}

// truncateLog 给日志里的长字符串做单行截断，避免 base64/JSON 把日志冲花。
func truncateLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
