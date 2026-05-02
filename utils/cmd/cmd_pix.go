package cmd

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/imgcut"
	"qq_bot/utils/zap"
)

func CmdPix(client *http.Client, argv []string, group_id int64) (err error) {
	var filename string
	var localPath string
	var resp *http.Response
	for {
		keyword := ""
		r18 := 0
		if len(argv) >= 2 {
			keyword = argv[1]
			zap.Logger.Debugf("接收到keyword:%s", keyword)
		}
		if len(argv) >= 3 {
			r := argv[2]
			if r == "r18" {
				r18 = 1
			} else {
				r18 = 0
			}
		}
		if keyword == "r18" {
			keyword = ""
			r18 = 1
		}
		zap.Logger.Debugf(conf.Cfg.Pixiv.PixivAddress)
		pid, _, url, err := logic.GetPixivPidTitleUrl(client, group_id, keyword, r18)
		if err != nil {
			return err
		}
		filename = fmt.Sprintf("%d.jpg", pid)
		// 缓存目录默认在 os.TempDir()/qq-bot/jm，先确保存在
		_ = os.MkdirAll(conf.Cfg.Cache.TmpDir, 0755)
		localPath = filepath.Join(conf.Cfg.Cache.TmpDir, filename)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}
		resp, err = client.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusOK {
			break
		}
	}

	global.TmpMtx.RLock()
	defer global.TmpMtx.RUnlock()
	file, err := os.Create(localPath)

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return err
	}
	err = imgcut.ImgCut(localPath)
	if err != nil {
		return err
	}

	err, yes := logic.CanSendImage(client)
	if err != nil {
		return err
	}
	if !yes {
		return errors.New("qq无法发送图片")
	}

	file.Close()

	if err = logic.UploadGroupFile(client, group_id, localPath, filename); err != nil {
		return err
	}

	return nil
}
