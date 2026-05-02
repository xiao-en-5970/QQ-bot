package global

import (
	"errors"
	"sync"

	"github.com/panjf2000/ants/v2"
	"qq_bot/model"
	"qq_bot/utils/dedup"
	"qq_bot/utils/kimi"
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
	ErrCmdMenu         = "当前指令有:\njm（jm本子）;\nhelp（帮助）;\npix（pixiv图片）;\ngithub（项目开源）\n" + ErrCmdHelpHelp
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

	//help错误
	ErrCmdHelpHelp = "格式：@bot help 功能[jm,pix等,可留空]\n例：@bot help jm"
)

var (
	ChanToParseCmd = make(chan model.ChanToParseCmd, 15)
	Wg             = &sync.WaitGroup{}
	TmpMtx         = &sync.RWMutex{}
	ThreadPool, _  = ants.NewPool(20)

	// Kimi 是 Moonshot AI 句柄；如果配置里没填 GPT_API_KEY 就是 nil，CmdDefault 会回退打印菜单
	Kimi *kimi.Kimi

	// ProcessedMsgIDs 记录已经处理过的群消息 message_id，防止重复回复。
	// WS 模式下 NapCat 单连接不会重复推同一事件，但断线重连 / 跑两个 bot 实例时仍能兜底。
	// 容量 4096 远超日常需要；message_id 是单调增长的，旧的可以安全淘汰。
	ProcessedMsgIDs = dedup.NewLRUSet(4096)
)
