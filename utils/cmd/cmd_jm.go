package cmd

import (
	"fmt"
	"net/http"
	"os/exec"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/file_operate"
	"qq_bot/utils/to_pdf"
	"qq_bot/utils/to_zip"
	zaplog "qq_bot/utils/zap"
	"strconv"
	"strings"
)

func CmdJm(client *http.Client, dataSlice []string, group_id int64, user_id int64, chapter int64) (err error) {
	if len(dataSlice) < 2 {
		zaplog.Logger.Warnf("Arg error: %v", err)
		_ = logic.SendGroupAtText(client, group_id, user_id, fmt.Sprintf("jm%s\n%s", global.ErrCmdArgFault, global.ErrCmdJmHelp))
		return nil
	}
	num, err := strconv.ParseInt(dataSlice[1], 10, 64)
	if err != nil {
		zaplog.Logger.Warnf("Arg parse error: %v", err)
		_ = logic.SendGroupAtText(client, group_id, user_id, fmt.Sprintf("jm%s\n%s", global.ErrCmdArgFault, global.ErrCmdJmHelp))
		return nil
	}

	if len(dataSlice) > 2 {
		chapter, err = strconv.ParseInt(dataSlice[2], 10, 64)
		if err != nil {
			zaplog.Logger.Warnf("Arg parse error: %v", err)
			return err
		}
	}

	_ = logic.SendGroupAtText(client, group_id, user_id, fmt.Sprintf("%s %d 第 %d 章", global.InfoCmdJmFindingBook, num, chapter))
	err = Jmcomic(client, group_id, user_id, num, chapter)
	if err != nil {
		return err
	}
	return nil
}

func Jmcomic(client *http.Client, group_id int64, user_id int64, number int64, chapter int64) (err error) {

	//判断缓存里面是否存在之前搜过的本子
	err, exist := file_operate.FindCache(fmt.Sprintf("./pdftmp/%d_%d.pdf", number, chapter))
	if exist {
		err = logic.UploadGroupFile(client, group_id, fmt.Sprintf("./pdftmp/%d_%d.pdf", number, chapter), fmt.Sprintf("%d_%d.pdf", number, chapter))
		if err != nil {
			zaplog.Logger.Warn(err)
		}
	} else {

		zaplog.Logger.Infof("接收到番号 %d 第 %d 章", number, chapter)
		if err = to_zip.MkDir("./tmp"); err != nil {
			zaplog.Logger.Warnf("创建./tmp失败: %v", err)
		}
		cmd := exec.Command("./package/jmcomic.exe", fmt.Sprint(number), "--option=./package/jmoption/opt.yml")
		// 运行命令并获取输出结果
		output, err := cmd.CombinedOutput()
		if err != nil {
			zaplog.Logger.Warnf("执行命令时发生错误/中途退出: %v", err)
			_ = logic.SendGroupAtText(client, group_id, user_id, global.ErrCmdJmUnknownFault)
			return err
		}
		var builder strings.Builder
		builder.Write(output)

		zaplog.Logger.Debugf("命令输出结果:%s", builder.String())
		//如果调用jm接口没报错
		if strings.HasPrefix(builder.String(), "Exception") {
			zaplog.Logger.Warnf("jm %d 查找出了未知问题", number)
			//logic.SendGroupAtText(client, group_id, user_id, global.ErrCmdJmUnknownFault)

		}

		err = to_pdf.ToPdf(fmt.Sprintf("./tmp/%d/%d", number, chapter), fmt.Sprintf("./pdftmp/%d_%d.pdf", number, chapter))
		if err != nil {
			zaplog.Logger.Warn(err)
			_ = logic.SendGroupAtText(client, group_id, user_id, global.ErrCmdJmNotFound)
		}
		err = logic.UploadGroupFile(client, group_id, fmt.Sprintf("./pdftmp/%d_%d.pdf", number, chapter), fmt.Sprintf("%d_%d.pdf", number, chapter))
		if err != nil {
			zaplog.Logger.Warn(err)
		}

		//本次不采用转换为zip，因为群u觉得麻烦
		//err = to_zip.Tozip("./tmp", "./ziptmp", fmt.Sprintf("%d.zip", number))
		//if err != nil {
		//	zaplog.Logger.Error(err)
		//}
		//err = logic.UploadGroupFile(client, group_id, fmt.Sprintf("./ziptmp/%d.zip", number), fmt.Sprintf("%d.zip", number))
		//if err != nil {
		//	zaplog.Logger.Error(err)
		//}
		//zaplog.Logger.Infof("%d.zip上传成功\n", number)
		err = to_zip.RemoveDir(fmt.Sprintf("./tmp/%d", number))
		if err != nil {
			zaplog.Logger.Warn(err)
		}
	}
	return nil
}
