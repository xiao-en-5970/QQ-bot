package logic

import (
	"errors"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/model"
	ser "qq_bot/service"
	zaplog "qq_bot/utils/zap"
	"strconv"
)

func GetNewAtMessage(client *http.Client, group_id int64, LatestSeq *int64) (err error) {
	err, resp := ser.GetGroupMsgHistory(client, &model.GetGroupMsgHistoryReq{
		GroupID: group_id,
	})
	if err != nil {
		zaplog.Logger.Errorf("群号:%d,err:%v", group_id, err)
		return err
	}
	// 获取切片的长度
	length := len(resp.Data.Messages)
	// 检查切片是否为空
	if length == 0 {
		zaplog.Logger.Debugf("群历史消息为空,群号:%d", group_id)
		*LatestSeq = 0
		return errors.New("slice is empty")
	}
	//如果第一次查找，则直接以最新未读消息为基准划分未读已读消息
	if *LatestSeq == 0 {
		*LatestSeq = resp.Data.Messages[length-1].MessageSeq - 1
		zaplog.Logger.Debugf("LatestSeq init successfully! LatestSeq: %d GroupID:%v", *LatestSeq, group_id)

	}
	//如果没有未读消息，则返回
	if *LatestSeq == resp.Data.Messages[length-1].MessageSeq {
		//zaplog.Logger.Debugf("消息已经为最新消息 LatestSeq: %d GroupID:%v", *LatestSeq, group_id)
		return nil
	}
	//遍历未读消息
	for _, msg := range resp.Data.Messages {
		userID := msg.UserID
		//如果消息是未读
		if msg.MessageSeq > *LatestSeq {
			for index, singleSlice := range msg.Message {

				if index == 0 {
					//如果开头不是at，就不处理，break掉
					if singleSlice.Type != "at" {
						break
					} else {
						if len(msg.Message) == 1 {
							zaplog.Logger.Debugln("return menu")
							_ = SendGroupAtText(client, group_id, userID, global.ErrCmdMenu)
							break
						}
						//如果是at，则检查at的对象是不是bot，如果不是，则break
						d := singleSlice.Data.(map[string]interface{})
						strid := d["qq"].(string)
						if strid == "all" {
							break
						}
						id, err := strconv.ParseInt(strid, 10, 64)
						if err != nil {
							zaplog.Logger.Error(err.Error())
							return err
						}
						zaplog.Logger.Debugf("id:%d userId:%d", id, *conf.Cfg.User.UserID)
						if id != *conf.Cfg.User.UserID {
							break
						}
					}
					//
				} else if index == 1 {
					if singleSlice.Type == "text" {
						zaplog.Logger.Debugf("index == 1 && is text ")
						data := model.TextData{}
						d := singleSlice.Data.(map[string]interface{})
						data.Text = d["text"].(string)
						zaplog.Logger.Debugf("data.Text:%v", data.Text)
						global.ChanToParseCmd <- model.ChanToParseCmd{
							GroupID: group_id,
							UserID:  userID,
							Data:    data,
						}
						zaplog.Logger.Debugf("global.ChanToJm <-，%#v", len(global.ChanToParseCmd))

					} else {
						zaplog.Logger.Debugln("return menu")
						_ = SendGroupAtText(client, group_id, userID, global.ErrCmdArgFault)

						break
					}
				} else {
					break
				}

			}
		}

	}
	*LatestSeq = resp.Data.Messages[length-1].MessageSeq
	return nil
}
