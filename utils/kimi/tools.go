package kimi

import (
	"context"

	openai "github.com/sashabaranov/go-openai"
)

// Tool 一个 LLM 可以通过 OpenAI 标准 function calling 调用的工具。
//
// 协议：LLM 收到带 Tools 的 Chat 请求后，可以选择"现在不答、先调工具"，返回的 message 里
// 带有 tool_calls 数组（每个 call 含 id / name / arguments JSON 字符串）。
// 我们本地用 Handler 跑出 result（普通字符串），然后以 role=tool 追加到 messages 里再问一次。
// 直到 LLM 不再调工具、给最终文本，循环结束。
//
// 设计要点：
//   - **不要给 LLM 高破坏性的工具**（下载 / 发消息 / 删数据）。LLM 会按需决定怎么用，
//     破坏性能力一旦被滥用 / 模型幻觉，影响范围会很大。优先做"只读"工具（查询 / 检索）。
//   - Handler 必须在 ctx 内有限时间返回，避免拖死 Chat() 的 60s 超时。
//   - Handler 输出 string 不宜超过几千字符，模型上下文有限；过长应自行截断 + 摘要。
type Tool struct {
	Spec    openai.Tool
	Handler func(ctx context.Context, arguments string) (string, error)
}

// registry 全局工具注册表。
//
// 各工具在自己的 init() 里调用 Register() 加入，避免运行期改 map 引发 race。
// 当前已注册的工具：
//   - read_skill: 读取 skill/ 目录下的 SKILL.md 文档（progressive disclosure）
//
// 后续要加新工具：建一个 tools_xxx.go，写好 spec/handler，init() 里 Register。
var registry = make(map[string]Tool)

// Register 在 init() 阶段把工具登记进来。
// 同名 Register 后注册的会覆盖前一个，方便测试桩；正常代码应当避免重名。
func Register(t Tool) {
	if t.Spec.Function == nil {
		return
	}
	registry[t.Spec.Function.Name] = t
}

// toolSpecs 把当前注册表打成 OpenAI 标准的 []openai.Tool。
// 没有任何工具时返回 nil（让 Chat 请求里 tools 字段直接 omit）。
func toolSpecs() []openai.Tool {
	if len(registry) == 0 {
		return nil
	}
	out := make([]openai.Tool, 0, len(registry))
	for _, t := range registry {
		out = append(out, t.Spec)
	}
	return out
}

// invokeTool 执行某个 tool_call，返回作为 role=tool 内容的字符串。
// 找不到对应工具或 Handler 报错时，把错误信息当 result 喂回 LLM（让模型自己看到错继续推理）。
func invokeTool(ctx context.Context, name, arguments string) string {
	t, ok := registry[name]
	if !ok {
		return "ERROR: bot 里没有这个工具 " + name + "，请检查工具名"
	}
	result, err := t.Handler(ctx, arguments)
	if err != nil {
		return "ERROR: 执行工具 " + name + " 失败: " + err.Error()
	}
	return result
}
