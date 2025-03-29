package cmdline

import (
	"errors"
	"fmt"
	"os"
	zaplog "qq_bot/utils/zap"
	"strconv"
)

func GetCmdLine() (err error, num int64) {
	// 检查是否提供了至少一个参数
	if len(os.Args) < 2 {
		zaplog.Logger.Infof("未获取命令行参数")
		return errors.New("未获取命令行参数"), 0
	}
	// 获取第一个参数
	arg := os.Args[1]

	// 将字符串参数转换为int64
	num, err = strconv.ParseInt(arg, 10, 64)
	if err != nil {
		fmt.Printf("无法转换 '%s' to int64: %v", arg, err)
		return err, 0
	}
	return nil, num
}
