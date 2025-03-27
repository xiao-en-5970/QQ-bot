package to_zip

import (
	"archive/zip"
	"io"
	zaplog "qq_bot/utils/zap"

	"fmt"
	"os"
	"path/filepath"
)

func Tozip(srcDir string, destDir string, ZipName string) (err error) {
	// 确保目标目录存在
	err = os.MkdirAll(destDir, os.ModePerm)
	if err != nil {
		zaplog.Logger.Warnf("创建目标目录失败: %v", err)
		return err
	}
	// 创建目标 ZIP 文件
	err = CreateZip(srcDir, destDir+"/"+ZipName)
	if err != nil {
		zaplog.Logger.Warnf("创建 ZIP 文件失败: %v", err)
		return err
	}

	return nil
}

func RemoveDir(dirPath string) (err error) {
	return os.RemoveAll(dirPath)
}

func MkDir(dirPath string) (err error) {
	return os.MkdirAll(dirPath, os.ModePerm)
}

func CreateZip(srcDir, destZip string) error {
	// 创建目标 ZIP 文件
	file, err := os.Create(destZip)
	if err != nil {
		zaplog.Logger.Errorf("无法创建 ZIP 文件: %w", err)
		return fmt.Errorf("无法创建 ZIP 文件: %w", err)
	}
	defer file.Close()

	// 创建一个新的 ZIP 写入器
	zipWriter := zip.NewWriter(file)
	defer zipWriter.Close()

	// 遍历 srcDir 目录中的所有文件和子目录
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			zaplog.Logger.Errorf("遍历路径失败: %w", err)
			return fmt.Errorf("遍历路径失败: %w", err)
		}

		// 跳过目录本身
		if info.IsDir() {
			return nil
		}

		// 打开要压缩的文件
		srcFile, err := os.Open(path)
		if err != nil {
			zaplog.Logger.Errorf("无法打开文件 %s: %w", path, err)
			return fmt.Errorf("无法打开文件 %s: %w", path, err)
		}
		defer srcFile.Close()

		// 获取文件在 ZIP 中的相对路径
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			zaplog.Logger.Errorf("获取相对路径失败: %w", err)
			return fmt.Errorf("获取相对路径失败: %w", err)
		}

		// 创建一个新的 ZIP 文件头
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			zaplog.Logger.Errorf("无法创建文件头: %w", err)
			return fmt.Errorf("无法创建文件头: %w", err)
		}
		header.Name = relPath

		// 使用 Deflate 压缩方法
		header.Method = zip.Deflate

		// 创建一个新的 ZIP 文件写入器
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			zaplog.Logger.Errorf("无法创建 ZIP 条目: %w", err)
			return fmt.Errorf("无法创建 ZIP 条目: %w", err)
		}

		// 将文件内容复制到 ZIP 文件中
		_, err = io.Copy(writer, srcFile)
		if err != nil {
			zaplog.Logger.Errorf("无法写入 ZIP 条目: %w", err)
			return fmt.Errorf("无法写入 ZIP 条目: %w", err)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("压缩文件失败: %w", err)
	}

	return nil
}

func GetFolderSize(dir string) (int64, error) {
	var size int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}
func ClearFolder(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// 跳过根目录本身
		if path == dir {
			return nil
		}
		// 删除文件或空文件夹
		return os.RemoveAll(path)
	})
}
