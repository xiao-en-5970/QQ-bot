package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/northes/go-moonshot"
)

// skillRoot 指向项目内 skill/ 目录的相对路径。
//
// CWD 在生产 Docker 是 /app，本地开发是项目根；两种情况下相对路径 "skill" 都能 resolve 到。
// 我们故意不读 conf——这条工具是给 Kimi 的"知识入口"，跟 bot 业务配置正交。
const skillRoot = "skill"

// 注册 read_skill 工具：让 Kimi 能"渐进式披露"地按需拉 skill 文档。
//
// 第一次调用建议传 path="" 或 path="hfut-union/SKILL.md" 拿索引；
// 模型自己根据用户问题决定下一步读哪个子 skill。
func init() {
	Register(Tool{
		Spec: &moonshot.ChatCompletionsTool{
			Type: moonshot.ChatCompletionsToolTypeFunction,
			Function: &moonshot.ChatCompletionsToolFunction{
				Name: "read_skill",
				Description: `读取 bot 内置 skill 文档（progressive disclosure）。

skill 文档按层级组织：
  - 最顶层 SKILL.md：列出所有可用 skill 入口
  - bot/SKILL.md：bot 自身能力 + 行为约束 + 自动回复策略 + 工具集说明（运行时主要看这一份）

调用建议：
1. 群里被触发、不熟悉 bot 边界 → read_skill(path="bot/SKILL.md")
2. 不知道有哪些 skill 可用 → read_skill(path="") 看顶层索引

参数 path 留空 = 顶层索引；非空 = skill 目录下的相对路径，例如 "bot/SKILL.md"。

⚠️ 路径白名单：仅允许 "" / "SKILL.md" / "bot" 子树（如 "bot/SKILL.md"）。
其它路径（如 hfut-union/）会被工具拒绝——业务能力必须经过 bot 工具集
（publish_good / off_shelf / publish_question 等）封装调用，
不要让模型直接生成 HTTP 调用、不要让模型读 hfut-union 那套 API 文档。`,
				Parameters: &moonshot.ChatCompletionsToolFunctionParameters{
					Type: moonshot.ChatCompletionsParametersTypeObject,
					Properties: map[string]*moonshot.ChatCompletionsToolFunctionProperties{
						"path": {
							Type:        "string",
							Description: "skill 目录下的相对路径，如 'hfut-union/SKILL.md' / 'hfut-union/post/SKILL.md' / 'bot/SKILL.md'。留空 = 读顶层索引（hfut-union/SKILL.md）",
						},
					},
					// path 不必填，留空就读顶层索引
				},
			},
		},
		Handler: readSkillHandler,
	})
}

// readSkillArgs 是 Kimi 在 tool_calls.function.arguments 里给的 JSON 反序列化结构。
type readSkillArgs struct {
	Path string `json:"path"`
}

// readSkillHandler 读取 skill 文件并把内容作为 tool result 返回。
//
// 安全约束：
//   - 必须落在 skillRoot 目录下，含 ".." / 绝对路径会被拒绝
//   - 文件不存在 / 越权时返回明确的错误描述（不抛 err，让 Kimi 看到错误自我修正下次调用）
//
// 大小约束：
//   - 单文件硬上限 32KB，超长截断 + 提示，避免把模型上下文撑爆
const skillReadMaxBytes = 32 * 1024

// isSkillPathAllowed 实现路径白名单：
//   - 顶层索引：rel 为空（已在外层规范成 "SKILL.md"）或 "SKILL.md"
//   - bot 子树：rel 等于 "bot" 或以 "bot/" 开头
//
// 其它一律拒绝（含 hfut-union/**）。注意 rel 是已经 TrimSpace 过的相对路径，
// 这里走纯字符串前缀匹配，简单可控；不依赖文件系统分隔符（slash 即可）。
func isSkillPathAllowed(rel string) bool {
	if rel == "" || rel == "SKILL.md" {
		return true
	}
	if rel == "bot" || strings.HasPrefix(rel, "bot/") {
		return true
	}
	return false
}

func readSkillHandler(ctx context.Context, arguments string) (string, error) {
	args := readSkillArgs{}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return fmt.Sprintf("ERROR: 解析 arguments 失败: %v (原始: %s)", err, arguments), nil
		}
	}
	rel := strings.TrimSpace(args.Path)
	if rel == "" {
		// 留空默认读最顶层 skill 索引
		rel = "SKILL.md"
	}

	// 安全 1：拒绝绝对路径 / 拒绝穿越 ".."
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return fmt.Sprintf("ERROR: 路径 %q 不允许（必须是 skill/ 下的相对路径，且不能含 ..）", rel), nil
	}

	// 安全 2：白名单——只允许读最顶层 SKILL.md 和 bot/ 子树。
	// 业务能力（hfut-union/）只暴露给开发者参考，不让 Kimi 在运行时拼 HTTP 调用，
	// 必须走 bot 自己封装的工具集（publish_good / off_shelf 等）。
	if !isSkillPathAllowed(rel) {
		return fmt.Sprintf("ERROR: 路径 %q 不在允许范围内（仅允许 \"\" / \"SKILL.md\" / \"bot/**\"）。"+
			"如需调用 hfut 相关能力，请使用 bot 工具集中的 publish_good / off_shelf 等高层工具，"+
			"而不是读 hfut-union 那套 API 文档。", rel), nil
	}

	full := filepath.Join(skillRoot, rel)
	// Clean 后再二次校验它仍在 skillRoot 下，防止 symlink / 奇异路径绕过
	cleaned := filepath.Clean(full)
	if !strings.HasPrefix(cleaned, skillRoot+string(filepath.Separator)) && cleaned != skillRoot {
		return fmt.Sprintf("ERROR: 解析后的路径 %q 越界", cleaned), nil
	}

	data, err := os.ReadFile(cleaned)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Sprintf("ERROR: 文件不存在 %q。可能是路径写错；试试先 read_skill(path=\"\") 看顶层索引", rel), nil
		}
		return fmt.Sprintf("ERROR: 读取 %q 失败: %v", rel, err), nil
	}

	if len(data) > skillReadMaxBytes {
		// 不直接 ioutil 全读完截断；先告知 Kimi 文件被截断了
		return fmt.Sprintf("[skill 文件 %s 已被截断为前 %dKB；如需完整内容可在 source 注释里找原文件路径]\n\n%s",
			rel, skillReadMaxBytes/1024, string(data[:skillReadMaxBytes])), nil
	}
	return string(data), nil
}
