package model

// 基响应
type BaseResp struct {
	Status  string `json:"status"`
	RetCode int    `json:"retcode"`
	Message string `json:"message"`
	Wording string `json:"wording"`
	Echo    string `json:"echo,omitempty"`
}

// 基请求
type BaseReq struct {
}

type BaseInterface interface {
	GetReq() interface{}
	GetResp() interface{}
	Name() string
}
