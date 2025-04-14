package logic

import (
	"errors"
	"net/http"
	url1 "net/url"
	"qq_bot/global"
	"qq_bot/model"
	"qq_bot/service"
	"qq_bot/utils/zap"
	"strconv"
)

func GetPixivPidTitleUrl(client *http.Client, groupid int64, keyword string, r18 int) (pid int64, title string, url string, err error) {
	formData := url1.Values{}
	formData.Set("keyword", keyword)
	formData.Set("r18", strconv.Itoa(r18))
	err, resp := service.GetPixivImage(client, &model.GetPixivImageReq{}, formData)
	if err != nil {
		return 0, "", "", err
	}
	if len(*resp) == 0 {
		err = errors.New(global.ErrCmdPixTagNotFound + keyword)
		SendGroupText(client, groupid, global.ErrCmdPixTagNotFound+keyword)
		return 0, "", "", err
	}
	zap.Logger.Debugf("resp: %v", (*resp)[0])
	return (*resp)[0].Pid, (*resp)[0].Title, (*resp)[0].Url, nil
}
