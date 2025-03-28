package global

import (
	"qq_bot/model"
	"sync"
)

const (
	//基础错误
	ErrCmdNotFound     = "无法识别该指令格式"
	ErrCmdArgFault     = "指令参数错误"
	ErrCmdMenu         = "当前指令有jm,help"
	ErrCmdUnknownFault = "指令未知错误"

	//jm错误
	ErrCmdJmHelp         = "例：@bot jm 123456 2\n(番号123456的第2章)"
	ErrCmdJmUnknownFault = "jm" + ErrCmdUnknownFault
	ErrCmdJmNotFound     = "未查找到番号对应的本子..."

	InfoCmdJmFindingBook = "...正在查找本子"
)

var (
	ChanToJm = make(chan model.ChanToJM, 10)
	Wg       sync.WaitGroup
)
