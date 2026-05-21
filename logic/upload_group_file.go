package logic

import (
	"net/http"
	"path/filepath"
	"qq_bot/global"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// UploadGroupFile 调 NapCat /upload_group_file 把本地文件上传到群文件区。
//
// NapCat 接受的 file 字段允许：
//   - 本地绝对路径
//   - file:// URI
//   - http(s):// URL
// 这里走绝对路径，是 NapCat 文档的默认建议形式。
//
// 灰度静默 (SilentMode) 开启 + 目标群非运维群时直接返回 nil，不发 NapCat。
// 详见 logic/silent_gate.go。
func UploadGroupFile(client *http.Client, groupID int64, file string, name string) error {
	if silentSuppressGroupFile(groupID, name) {
		return nil
	}
	zaplog.Logger.Infof("正在上传文件 %s 到 group=%d", name, groupID)

	global.TmpMtx.RLock()
	defer global.TmpMtx.RUnlock()

	absolutePath, err := filepath.Abs(file)
	if err != nil {
		zaplog.Logger.Errorf("无法解析文件绝对路径 file=%s: %v", file, err)
		return err
	}

	err, _ = service.UploadGroupFile(client, &model.UploadGroupFileReq{
		GroupID: groupID,
		File:    absolutePath,
		Name:    name,
	})
	if err != nil {
		zaplog.Logger.Errorf("upload_group_file failed group=%d file=%s: %v", groupID, name, err)
		return err
	}
	zaplog.Logger.Infof("%s 上传成功", name)
	return nil
}
