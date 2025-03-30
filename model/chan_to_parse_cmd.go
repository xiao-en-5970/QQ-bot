package model

type ChanToParseCmd struct {
	GroupID int64
	UserID  int64
	Data    TextData
}

type ChanToUpdateGroupList struct {
	GroupID int64
}
