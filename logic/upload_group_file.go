package logic

import (
	"net/http"
	"qq_bot/global"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// UploadGroupFile 调 NapCat /upload_group_file 把本地文件上传到群文件区。
//
// file 支持：
//   - 本地路径 / file:// 本地 URI（会被读出内容内联成 base64://，因为 NapCat 现在
//     跑在远程主机上、读不到 bot 本地磁盘；详见 napCatFileRef）
//   - http(s):// URL（NapCat 自己去下载）
//
// ⚠️ 大文件（如 jm 生成的大体积漫画 PDF）用 base64 内联有 OOM / socket 断裂风险
// （NapCat 已知问题），且会撞 nginx client_max_body_size / NapCat 请求体上限。
// 如果 jm 常传大文件，建议后续改成「bot 侧起 http 资源服务 + 传 URL」的方式。
//
// 灰度静默 (SilentMode) 开启 + 目标群非运维群时直接返回 nil，不发 NapCat。
// 详见 logic/silent_gate.go。
func UploadGroupFile(client *http.Client, groupID int64, file string, name string) error {
	if silentSuppressGroupFile(groupID, name) {
		return nil
	}
	zaplog.Logger.Infof("正在上传文件 %s 到 group=%d", name, groupID)

	// 读文件转 base64 要在锁内完成，避免缓存清理协程（持写锁）中途把文件删了
	global.TmpMtx.RLock()
	defer global.TmpMtx.RUnlock()

	ref, err := napCatFileRef(file)
	if err != nil {
		zaplog.Logger.Errorf("准备上传文件失败 file=%s: %v", file, err)
		return err
	}

	err, _ = service.UploadGroupFile(client, &model.UploadGroupFileReq{
		GroupID: groupID,
		File:    ref,
		Name:    name,
	})
	if err != nil {
		zaplog.Logger.Errorf("upload_group_file failed group=%d file=%s: %v", groupID, name, err)
		return err
	}
	zaplog.Logger.Infof("%s 上传成功", name)
	return nil
}
