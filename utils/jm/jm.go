package jm

import (
	"context"
	"fmt"
	"os/exec"
	"qq_bot/global"
	zaplog "qq_bot/utils/zap"
)

func Jmcomic(ctx context.Context) {
	//fmt.Println(os.Getwd())
	defer global.Wg.Done()
	select {
	case <-ctx.Done():
		return
	case num := <-global.ChanToJm:
		// 使用range持续接收，直到通道关闭
		zaplog.Logger.Infof("接收到 %d\n", num)
		cmd := exec.Command("./package/jmcomic.exe ", fmt.Sprint(num), "--option=./package/jmoption/opt.yml")
		// 运行命令并获取输出结果
		output, err := cmd.CombinedOutput()

		if err != nil {
			zaplog.Logger.Warnf("执行命令时发生错误: %v", err)
			return
		}
		// 将输出结果转换为字符串并打印
		zaplog.Logger.Infof("命令输出结果:%s", string(output))
	}
	return
}
