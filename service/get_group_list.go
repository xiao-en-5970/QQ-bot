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

func GetGroupList(client *http.Client, messageReq *model.GetGroupListReq) (err error, messageResp *model.GetGroupListResp) {
	//zaplog.Logger.Info("messageReq:", messageReq)
	data, err := json.Marshal(messageReq)
	if err != nil {
		zaplog.Logger.Error(err.Error())
		return err, nil
	}
	err, req := utils.NewReq(conf.Cfg.Address+"get_group_list", data)
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
	messageResp = new(model.GetGroupListResp)
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
	//zaplog.Logger.Infof("%#v", messageResp)
	return nil, messageResp
}
