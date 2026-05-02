package to_pdf

import (
	"os"
	"os/exec"
	"path/filepath"
	"qq_bot/conf"
	"qq_bot/utils/file_operate"
	zaplog "qq_bot/utils/zap"
	"strings"
)

// ToPdf 把 sourceDir 下所有图片合并成一个 PDF。
//
// 调用 conf.Tools.Img2pdfBin 决定使用哪个 img2pdf：
//   - Windows 默认 ./package/img2pdf.exe（PyInstaller 打包版）
//   - Linux   默认 img2pdf（pip install img2pdf 后的 CLI，参数与 .exe 完全一致）
//   - 如果 Img2pdfBin 后缀是 .py，则走 PythonBin <script.py> ...
//
// 命令模板：<img2pdf> <file1> <file2> ... -o <out.pdf> --pillow-limit-break
func ToPdf(sourceDir string, destFile string) (err error) {
	pdfDir := conf.Cfg.Cache.PdfTmpDir
	if pdfDir == "" {
		pdfDir = filepath.Join(os.TempDir(), "qq-bot", "pdf")
	}
	if err = os.MkdirAll(pdfDir, os.ModePerm); err != nil {
		zaplog.Logger.Error(err)
		return err
	}

	files, err := file_operate.GetAllFiles(sourceDir)
	if err != nil {
		return err
	}
	args := append([]string{}, files...)
	args = append(args, "-o", destFile, "--pillow-limit-break")

	bin := conf.Cfg.Tools.Img2pdfBin
	var cmd *exec.Cmd
	if strings.HasSuffix(strings.ToLower(bin), ".py") {
		// 用户把 Img2pdfBin 配成 Python 脚本时，前面拼一段 python3 解释器
		py := conf.Cfg.Tools.PythonBin
		if py == "" {
			py = "python3"
		}
		cmd = exec.Command(py, append([]string{bin}, args...)...)
	} else {
		cmd = exec.Command(bin, args...)
	}

	output, err := cmd.CombinedOutput()
	zaplog.Logger.Debugf("img2pdf 输出: %s", output)
	if err != nil {
		zaplog.Logger.Errorf("img2pdf 执行失败 bin=%s err=%v", bin, err)
		_ = os.RemoveAll(destFile)
		return err
	}
	return nil
}
