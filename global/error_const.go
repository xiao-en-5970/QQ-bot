package global

const (
	//基础错误
	ErrCmdNotFound     = "无法识别该指令格式"
	ErrCmdArgFault     = "指令参数错误"
	ErrCmdMenu         = "当前指令有jm,help"
	ErrCmdUnknownFault = "指令未知错误"

	//jm错误
	ErrCmdJmHelp         = "例：@bot jm 123456"
	ErrCmdJmUnknownFault = "jm" + ErrCmdUnknownFault
)
