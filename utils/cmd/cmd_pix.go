package cmd

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/imgcut"
	"qq_bot/utils/zap"
	"time"
)

func CmdPix(client *http.Client, argv []string, group_id int64) (err error) {
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
	filename := fmt.Sprintf("%d.jpg", pid)
	filepath := "./tmp/" + filename
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		logic.SendGroupText(client, group_id, global.ErrCmdPix404+keyword)
		return errors.New(resp.Status)
	}
	defer func() {
		time.Sleep(5 * time.Second)
		os.RemoveAll(filepath)
	}()
	global.TmpMtx.RLock()
	defer global.TmpMtx.RUnlock()
	file, err := os.Create(filepath) // 可修改文件名和扩展名

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return err
	}
	err = imgcut.ImgCut(filepath)
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

	if err = logic.UploadGroupFile(client, group_id, filepath, filename); err != nil {
		return err
	}

	return nil
}
