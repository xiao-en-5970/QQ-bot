package logic

import (
	"net/http"
	"path/filepath"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

func UploadGroupFile(client *http.Client, group_id int64, file string, name string) (err error) {
	zaplog.Logger.Infof("正在上传文件%s", name)
	absolutePath, err := filepath.Abs(file)
	err, _ = service.UploadGroupFile(client, &model.UploadGroupFileReq{
		GroupID: group_id,
		File:    absolutePath,
		Name:    name,
	})
	if err != nil {
		zaplog.Logger.Fatalf("UploadGroupFile failed: %v", err)
		return err
	}
	zaplog.Logger.Infof("%s上传成功", name)
	return nil
}
