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
		zaplog.Logger.Fatalf("Usage: go run main.go <int64_value> or add group_id in test.yaml")
		return errors.New("usage: go run main.go <int64_value> or write group_id in test.yaml"), 0
	}

	// 获取第一个参数
	arg := os.Args[1]

	// 将字符串参数转换为int64
	num, err = strconv.ParseInt(arg, 10, 64)
	if err != nil {
		fmt.Printf("Error converting '%s' to int64: %v", arg, err)
		return err, 0
	}
	return nil, num
}
