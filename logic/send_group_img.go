package logic

import (
	"net/http"
	"path/filepath"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// SendGroupImage 通过 NapCat 的 OB11MessageImage 段发送一张图片。
//
// file 支持：
//   - 本地路径（会被转成绝对路径，再加 file:// 前缀以提高 NapCat 识别成功率）
//   - http(s):// URL
//   - file:// URI
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
	resolved, err := resolveImageRef(file)
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
		zaplog.Logger.Errorf("napcat send_group_msg(image) failed group=%d file=%s: %v", groupID, resolved, err)
		return err
	}
	return nil
}

// resolveImageRef 让任意本地路径变成 NapCat 喜欢的 file:// URI，URL / base64 / file 原样返回。
func resolveImageRef(ref string) (string, error) {
	switch {
	case startsWith(ref, "http://"), startsWith(ref, "https://"),
		startsWith(ref, "file://"), startsWith(ref, "base64://"):
		return ref, nil
	}
	abs, err := filepath.Abs(ref)
	if err != nil {
		return "", err
	}
	return "file://" + abs, nil
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
