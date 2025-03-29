package to_pdf

import (
	"os"
	"os/exec"
	"path/filepath"
	zaplog "qq_bot/utils/zap"
)

func ToPdf(sourceDir string, destFile string) (err error) {
	err = os.MkdirAll("./pdftmp", os.ModePerm)
	if err != nil {
		zaplog.Logger.Error(err)
		return err
	}

	strSlice, err := GetAllFiles(sourceDir)
	strSlice = append(strSlice, "-o")
	strSlice = append(strSlice, destFile)
	strSlice = append(strSlice, "--pillow-limit-break")

	cmd := exec.Command("./package/img2pdf.exe", strSlice...)
	output, err := cmd.CombinedOutput()
	zaplog.Logger.Debugf("命令输出结果:%s", output)
	if err != nil {

		zaplog.Logger.Error(err)
		_ = os.RemoveAll(destFile)
		return err
	}

	return nil
}

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
