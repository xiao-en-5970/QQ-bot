package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/file_operate"
	"qq_bot/utils/to_pdf"
	"qq_bot/utils/to_zip"
	zaplog "qq_bot/utils/zap"
	"strconv"
	"strings"
)

// jmPaths 把缓存路径集中算一次，避免重复 filepath.Join。
type jmPaths struct {
	tmpRoot   string // jm 下载根目录，对应 conf.Cache.TmpDir
	pdfRoot   string // pdf 缓存根目录，对应 conf.Cache.PdfTmpDir
	chapDir   string // {tmpRoot}/{number}/{chapter}
	albumDir  string // {tmpRoot}/{number}
	pdfFile   string // {pdfRoot}/{number}_{chapter}.pdf
	pdfName   string // {number}_{chapter}.pdf（上传到群文件用的展示名）
	jmOption  string // 传给 jmcomic --option= 的 yml 路径，绝对路径以避开 cmd.Dir 影响
}

func newJmPaths(number, chapter int64) jmPaths {
	tmp := conf.Cfg.Cache.TmpDir
	pdf := conf.Cfg.Cache.PdfTmpDir
	optAbs, _ := filepath.Abs("./package/jmoption/opt.yml")
	return jmPaths{
		tmpRoot:  tmp,
		pdfRoot:  pdf,
		chapDir:  filepath.Join(tmp, fmt.Sprint(number), fmt.Sprint(chapter)),
		albumDir: filepath.Join(tmp, fmt.Sprint(number)),
		pdfFile:  filepath.Join(pdf, fmt.Sprintf("%d_%d.pdf", number, chapter)),
		pdfName:  fmt.Sprintf("%d_%d.pdf", number, chapter),
		jmOption: optAbs,
	}
}

func CmdJm(client *http.Client, dataSlice []string, group_id int64, user_id int64) (err error) {
	chapter := make([]int64, 0, 5)
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

	if len(dataSlice) == 3 {
		chapterStr := strings.Split(dataSlice[2], "-")
		var chapIntLeft, chapIntRight int64
		if len(chapterStr) == 0 {
			zaplog.Logger.Warnf("Arg parse error: %v", err)
			return errors.New("范围查询格式错误")
		} else if len(chapterStr) == 1 {
			chapIntLeft, err = strconv.ParseInt(chapterStr[0], 10, 64)
			if err != nil {
				zaplog.Logger.Warnf("Arg parse error: %v", err)
			}
			chapIntRight = chapIntLeft
			chapter = append(chapter, chapIntLeft)
		} else if len(chapterStr) == 2 {
			chapIntLeft, err = strconv.ParseInt(chapterStr[0], 10, 64)
			if err != nil {
				zaplog.Logger.Warnf("Arg parse error: %v", err)
			}
			chapIntRight, err = strconv.ParseInt(chapterStr[1], 10, 64)
			if err != nil {
				zaplog.Logger.Warnf("Arg parse error: %v", err)
			}
			for i := chapIntLeft; i <= chapIntRight; i++ {
				chapInt := i
				if err != nil {
					zaplog.Logger.Warnf("Arg parse error: %v", err)
					return err
				}
				chapter = append(chapter, chapInt)
			}
		} else {
			zaplog.Logger.Warnf("Arg parse error: %v", err)
			return errors.New("范围查询格式错误")
		}
	} else if len(dataSlice) >= 4 {
		for i := 2; i < len(dataSlice); i++ {
			chapInt, err := strconv.ParseInt(dataSlice[i], 10, 64)
			if err != nil {
				zaplog.Logger.Warnf("Arg parse error: %v", err)
				return err
			}
			chapter = append(chapter, chapInt)
		}
	} else if len(dataSlice) == 2 {
		chapter = append(chapter, 1)
	}

	_ = logic.SendGroupAtText(client, group_id, user_id, fmt.Sprintf("%s %d 第 %v 章", global.InfoCmdJmFindingBook, num, chapter))
	for idx, ch := range chapter {
		isEnd := false
		if idx == len(chapter)-1 {
			isEnd = true
		}
		err = Jmcomic(client, group_id, user_id, num, ch, isEnd)
		if err != nil {
			return err
		}
	}
	return nil
}

func Jmcomic(client *http.Client, group_id int64, user_id int64, number int64, chapter int64, isEnd bool) (err error) {
	global.TmpMtx.RLock()
	defer global.TmpMtx.RUnlock()

	p := newJmPaths(number, chapter)

	// 判断缓存里面是否存在之前搜过的本子
	err, exist := file_operate.FindCache(p.pdfFile)
	if exist {
		if err = logic.UploadGroupFile(client, group_id, p.pdfFile, p.pdfName); err != nil {
			zaplog.Logger.Warn(err)
			// 之前这里只 Warn 然后函数往后走 return nil，用户得不到任何反馈。
			// 现在上抛到 ExecCmd，让它兜底回执「指令 'jm' 执行失败: ...」。
			return err
		}
	} else {
		if !file_operate.IsDirExists(p.chapDir) {
			zaplog.Logger.Infof("接收到番号 %d 第 %d 章", number, chapter)
			if err = to_zip.MkDir(p.tmpRoot); err != nil {
				zaplog.Logger.Warnf("创建 %s 失败: %v", p.tmpRoot, err)
			}
			// jmcomic 命令跨平台：Windows 默认用打包好的 .exe，Linux 走 pip install jmcomic 后的 CLI
			// 命令行/参数完全一致，由 conf.Tools.JmcomicBin 决定走哪个
			// 把 jm 缓存目录通过环境变量传给 opt.yml 里的 ${QQBOT_JM_DIR}，避免 jmcomic 把图下到当前目录
			cmd := exec.Command(conf.Cfg.Tools.JmcomicBin, fmt.Sprint(number), "--option="+p.jmOption)
			cmd.Env = append(os.Environ(), "QQBOT_JM_DIR="+p.tmpRoot)
			output, err := cmd.CombinedOutput()
			if err != nil {
				zaplog.Logger.Warnf("执行 jmcomic 出错/中途退出: %v\noutput=%s", err, output)
				_ = logic.SendGroupAtText(client, group_id, user_id, global.ErrCmdJmUnknownFault)
				return fmt.Errorf("%w: jmcomic 执行失败: %v", global.ErrUserNotified, err)
			}
			// jmcomic 真正打印了什么是排查的关键信息（番号不存在 / opt.yml 路径错 / 限流 等都从这里看）。
			// 之前是 Debug 级别，生产里 LOG_LEVEL=info 看不见，遇到问题完全不知道发生了啥。
			zaplog.Logger.Infof("jmcomic %d 输出: %s", number, string(output))
			if strings.HasPrefix(string(output), "Exception") {
				zaplog.Logger.Warnf("jm %d 查找出了未知问题", number)
				_ = logic.SendGroupAtText(client, group_id, user_id, global.ErrCmdJmNotFound)
				return nil
			}
			if !file_operate.IsDirExists(p.chapDir) {
				// jmcomic 退出 0 但 chapDir 没生成。区分两类原因：
				//   1. 上游 API 挂了：output 里会有 RequestRetryAllFailException
				//      （opt.yml 里写死的 API 域名失效，5 次重试全 404/5xx，跟番号无关）
				//   2. 番号/章节不存在 / opt.yml 路径配错 / QQBOT_JM_DIR 没生效 等
				if strings.Contains(string(output), "RequestRetryAllFailException") {
					zaplog.Logger.Warnf("jm %d: 上游 API 不可用 (RequestRetryAllFailException) -- 检查 opt.yml 的 API 域名",
						number)
					_ = logic.SendGroupAtText(client, group_id, user_id, global.ErrCmdJmAPIDown)
					return nil
				}
				zaplog.Logger.Warnf("jm %d 第 %d 章: jmcomic 退出 0 但 %s 不存在 (本子或章节不存在 / opt.yml 路径未生效?)",
					number, chapter, p.chapDir)
				_ = logic.SendGroupAtText(client, group_id, user_id, global.ErrCmdJmNotFoundChapter)
				return nil
			}
		}
		if err = to_pdf.ToPdf(p.chapDir, p.pdfFile); err != nil {
			zaplog.Logger.Warn(err)
			_ = logic.SendGroupAtText(client, group_id, user_id, global.ErrCmdJmUnknownFault)
			return fmt.Errorf("%w: to_pdf 失败: %v", global.ErrUserNotified, err)
		}
		if err = logic.UploadGroupFile(client, group_id, p.pdfFile, p.pdfName); err != nil {
			zaplog.Logger.Warn(err)
			return err
		}
	}

	if isEnd {
		latestChap := 0
		if file_operate.IsDirExists(p.albumDir) {
			latestChap = 1
			for {
				if !file_operate.IsDirExists(filepath.Join(p.tmpRoot, fmt.Sprint(number), fmt.Sprint(latestChap))) {
					break
				}
				latestChap++
			}
		}
		if latestChap > 0 {
			zaplog.Logger.Debugf("最新章节获取成功")
			_ = logic.SendGroupAtText(client, group_id, user_id, fmt.Sprintf("jm%d 最新章节连载至第%d章", number, latestChap-1))
		}
	}
	return nil
}
