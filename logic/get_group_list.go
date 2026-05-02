package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// GetGroupList 调 NapCat /get_group_list 拿当前 bot 加入的所有群。
//
// noCache=true 会跳过 NapCat 自身缓存（适合「定期刷新群列表」时使用）。
func GetGroupList(client *http.Client, noCache bool) (err error, groupIDs []int64) {
	err, resp := service.GetGroupList(client, &model.GetGroupListReq{
		NoCache: noCache,
	})
	if err != nil {
		zaplog.Logger.Errorf("get_group_list failed: %v", err)
		return err, nil
	}
	groupIDs = make([]int64, 0, len(resp.Data))
	for _, g := range resp.Data {
		groupIDs = append(groupIDs, g.GroupID)
	}
	return nil, groupIDs
}
