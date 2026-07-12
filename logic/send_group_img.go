package logic

import (
	"net/http"
	"strconv"

	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// SendGroupImage 通过 NapCat 的 OB11MessageImage 段发送一张图片。
//
// file 支持：
//   - 本地路径 / file:// 本地 URI（会被读出内容内联成 base64://，因为 NapCat 现在
//     跑在远程主机上，读不到 bot 本地磁盘；详见 napCatFileRef）
//   - http(s):// URL
//   - base64:// 数据
//
// summary 是图片外显文本，可空。
//
// 灰度静默 (SilentMode) 开启 + 目标群非运维群时直接返回 nil，不发 NapCat。
// 详见 logic/silent_gate.go。
func SendGroupImage(client *http.Client, groupID int64, file string, summary string) error {
	if silentSuppressGroup(groupID, "[image]"+summary) {
		return nil
	}
	resolved, err := napCatFileRef(file)
	if err != nil {
		return err
	}

	err, _ = service.SendGroupMsg(client, &model.SendGroupMsgReq{
		GroupID: groupID,
		Message: []model.MessageSegment{
			{
				Type: "image",
				Data: model.ImageData{
					File:    resolved,
					Summary: summary,
				},
			},
		},
	})
	if err != nil {
		// base64:// 引用可能很长，日志里只截断打印避免刷屏
		zaplog.Logger.Errorf("napcat send_group_msg(image) failed group=%d file=%s: %v", groupID, truncateRef(resolved), err)
		return err
	}
	return nil
}

// truncateRef 打日志用：base64:// 引用体积大，截断展示，其余原样。
func truncateRef(ref string) string {
	const max = 64
	if len(ref) <= max {
		return ref
	}
	return ref[:max] + "...(" + strconv.Itoa(len(ref)) + "B)"
}
