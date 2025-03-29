package logic

import (
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

func GetGroupList(client *http.Client, no_cache bool) (err error, group_ids []int64) {
	err, resp := service.GetGroupList(client, &model.GetGroupListReq{
		NoCache: no_cache,
	})

	if err != nil {
		zaplog.Logger.Fatalf("Msg send failed: %v", err)
		return err, nil
	}
	group_ids = make([]int64, 0, len(resp.Data))
	for _, v := range resp.Data {
		group_ids = append(group_ids, v.GroupID)
	}
	return nil, group_ids
}
