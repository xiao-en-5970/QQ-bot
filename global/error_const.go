package global

import (
	"errors"
	"strings"
	"sync"

	"qq_bot/conf"
	"qq_bot/model"
	"qq_bot/utils/dedup"
	"qq_bot/utils/hfut"
	"qq_bot/utils/kimi"

	"github.com/panjf2000/ants/v2"
)

// ErrUserNotified 表示「子命令已自行 SendGroupAtText/SendGroupText 把错误发给 QQ 群了」。
//
// 用法：子命令向用户发完友好提示后，把错误用 fmt.Errorf("%w: ...", global.ErrUserNotified, ...) 包一层
// 再返回；ExecCmd 用 errors.Is 识别到该 sentinel 时就不再追加自己那条「指令执行失败」回执，
// 避免群里出现两条提示。返回该错误的语义仍然是「这次执行失败、不要继续」，只是「已经告诉用户了」。
var ErrUserNotified = errors.New("user already notified via QQ")

const (
	//基础错误
	ErrCmdNotFound     = "无法识别该指令格式"
	ErrCmdArgFault     = "指令参数错误"
	ErrCmdUnknownFault = "指令未知错误"

	//jm错误
	ErrCmdJmHelp            = "格式：@bot jm 番号 章节[默认为1]\n例：@bot jm 123456 1\n@bot jm 123456 1-9"
	ErrCmdJmUnknownFault    = "jm" + ErrCmdUnknownFault
	ErrCmdJmNotFound        = "未查找到番号对应的本子..."
	ErrCmdJmNotFoundChapter = "未查找到章节对应的本子..."
	// ErrCmdJmAPIDown 区分"番号/章节不存在"和"jmcomic 上游 API 挂掉"两种 case：
	// jmcomic 输出里出现 RequestRetryAllFailException 时（5 次重试全 404 / 5xx），
	// 几乎一定是 opt.yml 配的 API 入口域名失效了，跟番号无关，要去更新 opt.yml。
	ErrCmdJmAPIDown      = "JM 上游 API 暂时不可用（不是你番号写错了），稍后再试或联系管理员更新 opt.yml 的 API 域名"
	InfoCmdJmFindingBook = "...正在查找本子"

	//github 打印
	InfoCmdGithubPrint = "项目已开源：https://github.com/xiao-en-5970/QQ-bot"

	//pixiv错误
	ErrCmdPixHelp        = "格式：@bot pix 关键词[可留空，r18] r18[默认留空]\n例：@bot pix hifumi r18"
	ErrCmdPixTagNotFound = "未找到相关pixiv图片,tag:"
	ErrCmdPix404         = "图片无法访问，请重试,tag"

	//chat（Kimi 聊天）
	ErrCmdChatHelp        = "格式：@bot chat 想说的话\n例：@bot chat 你最近怎么样"
	ErrCmdChatDisabled    = "聊天功能未启用（GPT_API_KEY 未配置）"
	ErrCmdChatEmpty       = "请在 `@bot chat ` 后面带上想说的话"
	ErrCmdChatKimiFault   = "我刚才走神了... 你再说一遍 (；´д｀)"
	InfoCmdChatChatPrefix = "\n"

	//help错误
	ErrCmdHelpHelp = "格式：@bot help 功能[jm,pix,chat 等,可留空]\n例：@bot help jm"
)

// Menu 动态生成菜单文本，根据 conf.Cfg.Commands.Enabled 决定列哪几条子命令。
//
// 返回 "" 时表示当前没有任何启用的命令——调用方应该跳过发送（让 bot 完全安静），
// 而不是发一段空菜单刷屏。
//
// 调用者：ExecCmd（未识别关键字 + 没配 default 时的兜底）、CmdHelp（@bot help）、
// 单独 @bot 时的菜单回执。各调用点都做了"空字符串就 skip"的处理。
func Menu() string {
	var items []string
	if conf.Cfg.Commands.IsEnabled("jm") {
		items = append(items, "jm（jm本子）")
	}
	if conf.Cfg.Commands.IsEnabled("help") {
		items = append(items, "help（帮助）")
	}
	if conf.Cfg.Commands.IsEnabled("pix") {
		items = append(items, "pix（pixiv图片）")
	}
	if conf.Cfg.Commands.IsEnabled("chat") {
		items = append(items, "chat（与 bot 聊天）")
	}
	if conf.Cfg.Commands.IsEnabled("github") {
		items = append(items, "github（项目开源）")
	}
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("当前指令有:\n")
	for _, s := range items {
		b.WriteString(s)
		b.WriteString(";\n")
	}
	// help 命令没启用就别再贴 help 的格式说明了，让用户更糊涂。
	if conf.Cfg.Commands.IsEnabled("help") {
		b.WriteString(ErrCmdHelpHelp)
	}
	return b.String()
}

var (
	ChanToParseCmd = make(chan model.ChanToParseCmd, 15)
	Wg             = &sync.WaitGroup{}
	TmpMtx         = &sync.RWMutex{}
	ThreadPool, _  = ants.NewPool(20)

	// Kimi 是 Moonshot AI 句柄；如果配置里没填 GPT_API_KEY 就是 nil，CmdDefault 会回退打印菜单
	Kimi *kimi.Kimi

	// Hfut 是 hfut 后端 /api/v1/bot/* 客户端；conf 里 HfutAPIURL+HfutAPIServiceToken 都填了
	// 才会被 main.go 初始化，否则保持 nil。
	// auto_reply::processSnapshot 调用前需判 nil——nil 时回退到"[识别测试] 占位 ack"路径，
	// 不真上架，方便没接 hfut 时仍能跑识别 demo。
	Hfut *hfut.Client

	// ProcessedMsgIDs 记录已经处理过的群消息 message_id，防止重复回复。
	// WS 模式下 NapCat 单连接不会重复推同一事件，但断线重连 / 跑两个 bot 实例时仍能兜底。
	// 容量 4096 远超日常需要；message_id 是单调增长的，旧的可以安全淘汰。
	ProcessedMsgIDs = dedup.NewLRUSet(4096)
)
