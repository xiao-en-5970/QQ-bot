package file_operate

import (
	"fmt"
	"os"
	"path/filepath"
	"qq_bot/conf"
	"qq_bot/global"
	zaplog "qq_bot/utils/zap"
)

func GetAllFiles(dirPath string) ([]string, error) {
	var jpgFiles = make([]string, 0, 20)

	// 使用filepath.Walk遍历目录
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// 检查是否为文件且后缀为.jpg（不区分大小写）
		if !info.IsDir() {
			jpgFiles = append(jpgFiles, path)
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return jpgFiles, nil
}

func FindCache(filePath string) (err error, exist bool) {
	// 使用 os.Stat 获取文件信息
	_, err = os.Stat(filePath)

	// 判断文件是否存在
	if os.IsNotExist(err) {
		zaplog.Logger.Debugf("缓存未命中[%s]", filePath)
		return nil, false
	} else if err != nil {
		// 其他错误
		zaplog.Logger.Warnf("检查文件时出错: %v", err)
		return err, false
	} else {
		// 文件存在
		zaplog.Logger.Debugf("缓存命中[%s]", filePath)
		return nil, true
	}
}

// 获取目录大小
func GetDirSize(dirPath string) (int64, error) {
	var size int64 = 0

	// 遍历目录
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0, fmt.Errorf("无法读取目录 %s: %w", dirPath, err)
	}

	for _, entry := range entries {
		fullPath := filepath.Join(dirPath, entry.Name())

		if entry.IsDir() {
			// 如果是子目录，递归计算大小
			subDirSize, err := GetDirSize(fullPath)
			if err != nil {
				return 0, err
			}
			size += subDirSize
		} else {
			// 如果是文件，获取文件大小
			info, err := entry.Info()
			if err != nil {
				return 0, fmt.Errorf("无法获取文件信息 %s: %w", fullPath, err)
			}
			size += info.Size()
		}
	}

	return size, nil
}

// 清空目录
func ClearDir(dirPath string) error {
	err := os.RemoveAll(dirPath)
	if err != nil {
		return fmt.Errorf("无法清空目录 %s: %w", dirPath, err)
	}
	// 重新创建空目录
	err = os.MkdirAll(dirPath, os.ModePerm)
	if err != nil {
		return fmt.Errorf("无法重新创建目录 %s: %w", dirPath, err)
	}
	return nil
}

// 清理缓存文件
func ClearCache(dirPath string) error {
	global.TmpMtx.RLock()
	defer global.TmpMtx.RUnlock()
	maxSize := conf.Cfg.Cache.MaxSize * 1024 * 1024
	size, err := GetDirSize(dirPath)
	if err != nil {
		zaplog.Logger.Errorf("错误: %v", err)
		return err
	}

	zaplog.Logger.Debugf("目录 %s 的大小为: %d 字节 (%.2f MB)", dirPath, size, float64(size)/1024/1024)

	// 检查是否超过最大大小
	if size > maxSize {
		zaplog.Logger.Infof("目录%s大小超过 %d MB，正在清空...", dirPath, size)
		err := ClearDir(dirPath)
		if err != nil {
			zaplog.Logger.Errorf("清空目录%s失败: %v", dirPath, err)
			return err
		}
		zaplog.Logger.Infof("目录%s已清空。", dirPath)

	} else {
		zaplog.Logger.Debugf("目录%s大小未超过 1000 MB，无需清空。", dirPath)
	}
	return nil
}

// 文件夹是否存在
func IsDirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		// 其他类型的错误（如权限问题）
		return false
	}
	return info.IsDir()
}
