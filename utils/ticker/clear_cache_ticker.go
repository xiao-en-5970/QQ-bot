package ticker

import (
	"context"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/utils/file_operate"
	zaplog "qq_bot/utils/zap"
	"time"
)

// ClearCacheTicker 必须由 main 在 `go ClearCacheTicker(...)` 之前调用 global.Wg.Add(1)，
// 协程内部只负责 Done（避免 main 抢跑 Wg.Wait race，详见 main.go 注释）。
func ClearCacheTicker(ctx context.Context) {
	defer global.Wg.Done()
	ticker := time.NewTicker(time.Duration(conf.Cfg.Cache.ClearInterval) * time.Second) // 120秒间隔
	defer ticker.Stop()                                                                 // 程序退出时停止
	zaplog.Logger.Debugf("协程ClearCacheTicker启动")
	defer zaplog.Logger.Debugf("协程ClearCacheTicker退出")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			zaplog.Logger.Infof("开始清理缓存")
			err := file_operate.ClearCache(conf.Cfg.Cache.PdfTmpDir)
			if err != nil {
				zaplog.Logger.Errorf("PdfTmpDir 缓存处理失败: %v", err)
			}
			err = file_operate.ClearCache(conf.Cfg.Cache.TmpDir)
			if err != nil {
				zaplog.Logger.Errorf("TmpDir 缓存处理失败: %v", err)
			}
			zaplog.Logger.Infof("缓存清理成功")
		}
	}
}
