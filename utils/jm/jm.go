package jm

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/to_pdf"
	"qq_bot/utils/to_zip"
	zaplog "qq_bot/utils/zap"
	"strings"
	"time"
)

var (
	client = &http.Client{Timeout: 120 * time.Second}
)

func Jmcomic(ctx context.Context) {

	//fmt.Println(os.Getwd())
	defer global.Wg.Done()
	defer zaplog.Logger.Infof("协程Jmcomic退出\n")
	for {
		select {
		case <-ctx.Done():
			return
		case chanRecv, ok := <-global.ChanToJm:
			if ok {
				zaplog.Logger.Infof("<- global.ChanToJm，%#v\n", len(global.ChanToJm))
				zaplog.Logger.Infof("接收到番号 %d 第 %d 章\n", chanRecv.Number, chanRecv.Chapter)
				if err := to_zip.MkDir("./tmp"); err != nil {
					zaplog.Logger.Warnf("创建./tmp失败: %v\n", err)
				}
				cmd := exec.Command("./package/jmcomic.exe", fmt.Sprint(chanRecv.Number), fmt.Sprintf("p%d", chanRecv.Chapter), "--option=./package/jmoption/opt.yml")
				// 运行命令并获取输出结果
				output, err := cmd.CombinedOutput()
				if err != nil {
					zaplog.Logger.Warnf("执行命令时发生错误/中途退出: %v\n", err)

				}
				var builder strings.Builder
				builder.Write(output)

				zaplog.Logger.Infof("命令输出结果:%s", builder.String())
				////如果调用jm接口没报错
				//if !strings.HasPrefix(builder.String(), "Exception") {
				//
				//
				//} else {
				//	zaplog.Logger.Warn(errors.New("jm cmd exec failed"))
				//	logic.SendGroupMsg(client, chanRecv.GroupID, chanRecv.UserID, global.ErrCmdJmUnknownFault)
				//}
				err = to_pdf.ToPdf(fmt.Sprintf("./tmp/%d/%d", chanRecv.Number, chanRecv.Chapter), fmt.Sprintf("./pdftmp/%d_%d.pdf", chanRecv.Number, chanRecv.Chapter))
				if err != nil {
					zaplog.Logger.Warn(err)

				}
				err = logic.UploadGroupFile(client, chanRecv.GroupID, fmt.Sprintf("./pdftmp/%d_%d.pdf", chanRecv.Number, chanRecv.Chapter), fmt.Sprintf("%d_%d.pdf", chanRecv.Number, chanRecv.Chapter))
				if err != nil {
					zaplog.Logger.Warn(err)
				}
				zaplog.Logger.Infof("%d_%d.pdf上传成功\n", chanRecv.Number, chanRecv.Chapter)

				//本次不采用转换为zip，因为群u觉得麻烦
				//err = to_zip.Tozip("./tmp", "./ziptmp", fmt.Sprintf("%d.zip", chanRecv.Number))
				//if err != nil {
				//	zaplog.Logger.Error(err)
				//}
				//err = logic.UploadGroupFile(client, chanRecv.GroupID, fmt.Sprintf("./ziptmp/%d.zip", chanRecv.Number), fmt.Sprintf("%d.zip", chanRecv.Number))
				//if err != nil {
				//	zaplog.Logger.Error(err)
				//}
				//zaplog.Logger.Infof("%d.zip上传成功\n", chanRecv.Number)
				err = to_zip.RemoveDir(fmt.Sprintf("./tmp/%d", chanRecv.Number))
				if err != nil {
					zaplog.Logger.Warn(err)
				}
			}

		}
	}

}
