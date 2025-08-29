package global

import (
	"github.com/panjf2000/ants/v2"
	"qq_bot/model"
	"sync"
)

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
	InfoCmdJmFindingBook    = "...正在查找本子"

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
	ChanToUpdateGroupList = make(chan struct{}, 1)
	ChanToParseCmd        = make(chan model.ChanToParseCmd, 15)
	Wg                    = &sync.WaitGroup{}
	TmpMtx                = &sync.RWMutex{}
	ActiveGroups          = make(map[int64]bool, 20)
	ThreadPool, _         = ants.NewPool(20)
)
