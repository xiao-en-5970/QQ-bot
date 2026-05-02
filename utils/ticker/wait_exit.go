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

// WaitExit 必须由 main 在 `go WaitExit(...)` 之前调用 global.Wg.Add(1)，
// 协程内部只负责 Done。把 Add 提到父协程是为了避免 main 抢跑到 Wg.Wait() 时
// counter 还是 0、Wait 立即返回的 race。
func WaitExit(cancel context.CancelFunc) {
	defer global.Wg.Done()
	zaplog.Logger.Debugf("协程WaitExit启动")
	defer zaplog.Logger.Debugf("协程WaitExit退出")
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
	fmt.Println("收到退出信号，开始优雅退出...")
	cancel() // 调用cancel函数来通知其他goroutine退出
}
