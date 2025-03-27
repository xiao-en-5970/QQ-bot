package wait_exit

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"qq_bot/global"
	"syscall"
)

func WaitExit(cancel context.CancelFunc) {
	defer global.Wg.Done()
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
	fmt.Println("收到退出信号，开始优雅退出...")
	cancel() // 调用cancel函数来通知其他goroutine退出
	return
}
