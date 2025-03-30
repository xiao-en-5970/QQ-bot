package to_pdf

import (
	"os"
	"os/exec"
	"qq_bot/utils/file_operate"
	zaplog "qq_bot/utils/zap"
)

func ToPdf(sourceDir string, destFile string) (err error) {
	err = os.MkdirAll("./pdftmp", os.ModePerm)
	if err != nil {
		zaplog.Logger.Error(err)
		return err
	}

	strSlice, err := file_operate.GetAllFiles(sourceDir)
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
