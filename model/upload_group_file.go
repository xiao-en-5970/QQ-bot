package model

import "qq_bot/conf"

// NapCat：上传群文件
//   POST {server}/upload_group_file
//   doc:  napcat.apifox.cn -> 文件相关 -> 上传群文件
//
// File 字段支持：
//   - 本地绝对路径
//   - file:// URI
//   - http(s):// URL
//   - base64:// 数据
// Folder 是上传到的群文件夹 ID，不传则上传到根目录。

type UploadGroupFileReq struct {
	BaseReq
	GroupID int64  `json:"group_id"`
	File    string `json:"file"`
	Name    string `json:"name"`
	Folder  string `json:"folder,omitempty"`
}

type UploadGroupFileData struct {
	FileID string `json:"file_id"`
}

type UploadGroupFileResp struct {
	BaseResp
	Data UploadGroupFileData `json:"data"`
}

func (r *UploadGroupFileResp) GetBaseResp() *BaseResp { return &r.BaseResp }

type UploadGroupFile struct {
	Req  *UploadGroupFileReq
	Resp *UploadGroupFileResp
}

func (g UploadGroupFile) Name() string {
	return conf.Cfg.Server.Address + "upload_group_file"
}

func (g UploadGroupFile) GetReq() interface{}  { return g.Req }
func (g UploadGroupFile) GetResp() interface{} { return g.Resp }
