package logic

import (
	"errors"
	"fmt"
	"net/http"
	"qq_bot/conf"
	"qq_bot/model"
	ser "qq_bot/service"
	zaplog "qq_bot/utils/zap"
	"strconv"
)

func GetNewAtMessage(client *http.Client, LatestSeq int64) (err error, NewLatestSeq int64) {
	err, resp := ser.GetGroupMsgHistory(client, &model.GetGroupMsgHistoryReq{
		GroupID: *conf.Cfg.GroupID,
	})
	if err != nil {
		zaplog.Logger.Error(err.Error())
		return err, LatestSeq
	}
	// 获取切片的长度
	length := int64(len(resp.Data.Messages))
	// 检查切片是否为空
	if length == 0 {
		fmt.Println("切片为空")
		return errors.New("slice is empty"), LatestSeq
	}
	//如果第一次查找，则直接以最新未读消息为基准划分未读已读消息
	if LatestSeq == 0 {
		NewLatestSeq = resp.Data.Messages[length-1].MessageSeq
		zaplog.Logger.Infof("LatestSeq init successfully! NewLatestSeq: %d\n", NewLatestSeq)
		return nil, NewLatestSeq
	}
	//如果没有未读消息，则返回
	if LatestSeq == resp.Data.Messages[length-1].MessageSeq {
		zaplog.Logger.Infof("Message is already latest NewLatestSeq: %d\n", LatestSeq)
		return nil, LatestSeq
	}
	//找到哪些是未读消息
	StartIndex := int64(0)
	if resp.Data.Messages[0].MessageSeq < LatestSeq {
		NewLatestSeq = resp.Data.Messages[length-1].MessageSeq
		zaplog.Logger.Infof("MessageSeq Update! NewLatestSeq: %d\n", NewLatestSeq)
		StartIndex = LatestSeq - resp.Data.Messages[0].MessageSeq
	}

	//遍历未读消息
	for i, msg := range resp.Data.Messages {
		//如果消息是未读
		if int64(i) >= StartIndex {
			for index, singleSlice := range msg.Message {

				if index == 0 {
					//如果开头不是at，就不处理，break掉
					if singleSlice.Type != "at" {
						break
					} else {
						//如果是at，则检查at的对象是不是bot，如果不是，则break
						d := singleSlice.Data.(map[string]interface{})
						strid := d["qq"].(string)
						id, err := strconv.ParseInt(strid, 10, 64)
						if err != nil {
							zaplog.Logger.Error(err.Error())
							return err, NewLatestSeq
						}
						zaplog.Logger.Infof("id:%d userId:%d\n", id, *conf.Cfg.UserID)
						if id != *conf.Cfg.UserID {
							zaplog.Logger.Infof("Not botId,id:%v", id)
							break
						}
					}
					//
				} else {
					if singleSlice.Type == "text" {
						data := model.TextData{}
						d := singleSlice.Data.(map[string]interface{})
						data.Text = d["text"].(string)
						err = ParseCmd(client, data)
						if err != nil {
							return err, NewLatestSeq
						}
					} else {
						break
					}
				}

			}
		}

	}
	return nil, NewLatestSeq
}
