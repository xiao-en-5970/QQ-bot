package service

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"qq_bot/conf"
	"qq_bot/model"
	"qq_bot/utils/new_req"
)

func BaseService(client *http.Client, ReqResp model.BaseInterface) (err error) {

	//zaplog.Logger.Info("messageReq:", messageReq)
	data, err := json.Marshal(ReqResp.GetReq())
	if err != nil {
		//zaplog.Logger.Error(err.Error())
		return err
	}

	err, req := new_req.NewReq(conf.Cfg.Server.Address+ReqResp.Name(), data)
	if err != nil {
		//zaplog.Logger.Error(err.Error())
		return err
	}

	resp, err := client.Do(req)

	if err != nil {
		//zaplog.Logger.Error(err.Error())
		return err
	}

	defer resp.Body.Close()

	respData, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		//zaplog.Logger.Error(err.Error())
		return err
	}

	err = json.Unmarshal(respData, ReqResp.GetResp())
	if err != nil {
		//zaplog.Logger.Error(err.Error())
		return err
	}
	//zaplog.Logger.Infof("%#v", messageResp)
	return nil
}
