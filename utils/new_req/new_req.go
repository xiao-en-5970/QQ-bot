package new_req

import (
	"bytes"
	"fmt"
	"net/http"
	url1 "net/url"
	"qq_bot/conf"
)

// NewReq 构造一个 NapCat HTTP API 请求（POST + JSON）。
//
// NapCat 的 OneBot11 HTTP server 路径规则与 LLOneBot / go-cqhttp 一致：
//   {server.address}{action}    例如 https://host/send_group_msg
// 鉴权可选：在 server.access_token 配置时通过 Authorization: Bearer <token> 携带。
func NewReq(targetURL string, jsonData []byte) (err error, req *http.Request) {
	if len(jsonData) == 0 {
		jsonData = []byte("{}")
	}
	req, err = http.NewRequest(http.MethodPost, targetURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建 NapCat 请求失败 url=%s: %w", targetURL, err), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token := conf.Cfg.Server.AccessToken; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return nil, req
}

// NewReqWithForm 给非 NapCat 的 form 接口使用（如 pixiv 第三方 API）。
func NewReqWithForm(targetURL string, form url1.Values) (err error, req *http.Request) {
	req, err = http.NewRequest(http.MethodPost, targetURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return fmt.Errorf("创建 form 请求失败 url=%s: %w", targetURL, err), nil
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return nil, req
}
