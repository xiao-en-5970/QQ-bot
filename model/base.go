package model

// BaseResp 是 NapCat 所有 OneBot11 接口统一的响应外壳。
//
// 字段对齐 NapCat 的 BaseResponse + go-cqhttp 兼容字段：
//   - Status / RetCode / Message / Wording 来自 OneBot11
//   - Echo 仅 WebSocket 调用 API 时存在
//   - Stream 是 NapCat 流式响应标记
type BaseResp struct {
	Status  string `json:"status"`
	RetCode int    `json:"retcode"`
	Message string `json:"message,omitempty"`
	Wording string `json:"wording,omitempty"`
	Echo    string `json:"echo,omitempty"`
	Stream  string `json:"stream,omitempty"`
}

// IsOK 判断 NapCat 业务是否成功，参考 NapCat / OneBot11 规范。
func (b *BaseResp) IsOK() bool {
	if b == nil {
		return true
	}
	if b.Status == "" {
		// 部分接口（pixiv 第三方）没有 status，默认认为成功
		return true
	}
	return b.Status == "ok" || b.RetCode == 0
}

// BaseReq 不再额外约束字段，每个接口请求体自行定义即可。
type BaseReq struct{}

// BaseInterface 抽象 NapCat 单个 API 的请求/响应/路径。
type BaseInterface interface {
	GetReq() interface{}
	GetResp() interface{}
	Name() string
}

// BaseRespAccessor 让 service 层能拿到统一的响应外壳，做 retcode 校验。
type BaseRespAccessor interface {
	GetBaseResp() *BaseResp
}
