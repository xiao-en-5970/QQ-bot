package ticker

import (
	"context"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/utils/file_operate"
	zaplog "qq_bot/utils/zap"
	"time"
)

func ClearCacheTicker(ctx context.Context) {
	global.Wg.Add(1)
	ticker := time.NewTicker(time.Duration(conf.Cfg.Cache.ClearInterval) * time.Second) // 120秒间隔
	defer ticker.Stop()                                                                 // 程序退出时停止
	defer global.Wg.Done()
	zaplog.Logger.Debugf("协程ClearCacheTicker启动")
	defer zaplog.Logger.Debugf("协程ClearCacheTicker退出")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := file_operate.ClearCache(conf.Cfg.Cache.PdfTmpDir)
			if err != nil {
				zaplog.Logger.Errorf("PdfTmpDir 缓存处理失败: %v", err)
			}
			err = file_operate.ClearCache(conf.Cfg.Cache.TmpDir)
			if err != nil {
				zaplog.Logger.Errorf("TmpDir 缓存处理失败: %v", err)
			}
		}
	}
}
