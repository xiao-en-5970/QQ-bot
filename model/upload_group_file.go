package model

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

type UploadGroupFile struct {
	Req  *UploadGroupFileReq
	Resp *UploadGroupFileResp
}

func (g UploadGroupFile) Name() string {
	return "upload_group_file"
}

func (g UploadGroupFile) GetReq() interface{} {
	return g.Req
}

func (g UploadGroupFile) GetResp() interface{} {
	return g.Resp
}
