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

func UploadGroupFile(client *http.Client, messageReq *model.UploadGroupFileReq) (err error, messageResp *model.UploadGroupFileResp) {
	//zaplog.Logger.Info("messageReq:", messageReq)
	data, err := json.Marshal(messageReq)
	if err != nil {
		zaplog.Logger.Error(err.Error())
		return err, nil
	}
	err, req := utils.NewReq(conf.Cfg.Address+"upload_group_file", data)
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
	messageResp = new(model.UploadGroupFileResp)
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
