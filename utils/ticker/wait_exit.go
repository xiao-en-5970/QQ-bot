package ticker

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"qq_bot/global"
	zaplog "qq_bot/utils/zap"
	"syscall"
)

func WaitExit(cancel context.CancelFunc) {
	global.Wg.Add(1)
	defer global.Wg.Done()
	zaplog.Logger.Debugf("协程WaitExit启动")
	defer zaplog.Logger.Debugf("协程WaitExit退出")
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
	fmt.Println("收到退出信号，开始优雅退出...")
	cancel() // 调用cancel函数来通知其他goroutine退出
	return
}
