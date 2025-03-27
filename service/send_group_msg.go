package service

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"qq_bot/conf"
	"qq_bot/model"
	"qq_bot/utils"
	zaplog "qq_bot/utils/zap"
)

func SendGroupMsg(client *http.Client, messageReq *model.SendGroupMsgReq) (err error, messageResp *model.SendGroupMsgResp) {
	//zaplog.Logger.Info("messageReq:", messageReq)
	data, err := json.Marshal(messageReq)
	if err != nil {
		zaplog.Logger.Error(err.Error())
		return err, nil
	}
	err, req := utils.NewReq(conf.Cfg.Address+"send_group_msg", data)
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
	messageResp = new(model.SendGroupMsgResp)
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

	return nil, messageResp
}
