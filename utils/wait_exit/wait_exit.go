package wait_exit

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
	defer global.Wg.Done()
	defer zaplog.Logger.Infof("协程WaitExit退出")
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
	fmt.Println("收到退出信号，开始优雅退出...")
	cancel() // 调用cancel函数来通知其他goroutine退出
	return
}
