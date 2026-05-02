package logic

import (
	"fmt"
	"net/http"
	"qq_bot/model"
	"qq_bot/service"
	zaplog "qq_bot/utils/zap"
)

// CheckNapCatAlive 启动期对 NapCat HTTP 服务做一次 /get_status 探测。
//
// 任意一个条件不满足都会返回 error，由调用方决定是否继续运行：
//   - HTTP 调用失败（NapCat 没启动或反代不通）
//   - status != "ok"
//   - data.online == false（QQ 没登录）
//   - data.good   == false（NapCat 自检失败）
func CheckNapCatAlive(client *http.Client) error {
	err, resp := service.GetStatus(client, &model.GetStatusReq{})
	if err != nil {
		return fmt.Errorf("调用 NapCat /get_status 失败: %w", err)
	}
	if !resp.Data.Online {
		return fmt.Errorf("NapCat 在线检测未通过：online=%v good=%v", resp.Data.Online, resp.Data.Good)
	}
	if !resp.Data.Good {
		zaplog.Logger.Warnf("NapCat /get_status 报告 good=false，可能存在不健康状态")
	}
	zaplog.Logger.Infof("NapCat 连通性检查通过 (online=%v good=%v)", resp.Data.Online, resp.Data.Good)
	return nil
}
