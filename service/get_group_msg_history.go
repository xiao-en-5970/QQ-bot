package service

import (
	"io/ioutil"
	"qq_bot/conf"
	"qq_bot/utils"
	zaplog "qq_bot/utils/zap"

	"encoding/json"
	"net/http"
	"qq_bot/model"
)

func GetGroupMsgHistory(client *http.Client, messageReq *model.GetGroupMsgHistoryReq) (err error, messageResp *model.GetGroupMsgHistoryResp) {
	//zaplog.Logger.Info("messageReq:", messageReq)
	data, err := json.Marshal(messageReq)
	if err != nil {
		zaplog.Logger.Error(err.Error())
		return err, nil
	}
	err, req := utils.NewReq(conf.Cfg.Address+"get_group_msg_history", data)
	if err != nil {
		zaplog.Logger.Error(err.Error())
		return err, nil
	}
	resp, err := client.Do(req)
	if err != nil {
		zaplog.Logger.Error(err.Error())
		return err, nil
	}
	defer resp.Body.Close()
	messageResp = new(model.GetGroupMsgHistoryResp)
	respData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		zaplog.Logger.Error(err.Error())
		return err, nil
	}

	err = json.Unmarshal(respData, messageResp)
	if err != nil {
		zaplog.Logger.Error(err.Error())
		return err, nil
	}
	zaplog.Logger.Infof("%#v", messageResp)
	return nil, messageResp
}
