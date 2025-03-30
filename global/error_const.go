package global

import (
	"qq_bot/model"
	"sync"
)

const (
	//基础错误
	ErrCmdNotFound     = "无法识别该指令格式"
	ErrCmdArgFault     = "指令参数错误"
	ErrCmdMenu         = "当前指令有:\njm（查找jm本子）,help（指令帮助）,github（查看项目开源）"
	ErrCmdUnknownFault = "指令未知错误"

	//jm错误
	ErrCmdJmHelp         = "例：@bot jm 123456 2\n(番号123456的第2章)"
	ErrCmdJmUnknownFault = "jm" + ErrCmdUnknownFault
	ErrCmdJmNotFound     = "未查找到番号对应的本子..."

	InfoCmdJmFindingBook = "...正在查找本子"

	//github 打印
	InfoCmdGithubPrint = "项目已开源：https://github.com/xiao-en-5970/QQ-bot"
)

var (
	ChanToUpdateGroupList = make(chan struct{}, 1)
	ChanToParseCmd        = make(chan model.ChanToParseCmd, 15)
	Wg                    sync.WaitGroup
	Mtx                   sync.Mutex
)
