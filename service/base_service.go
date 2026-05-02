package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	url1 "net/url"
	"qq_bot/model"
	"qq_bot/utils/new_req"
)

// BaseService 调用 NapCat 任意 OneBot11 接口（POST + JSON）。
//
// 流程：
//   1. 序列化请求体（即便请求体为空也会发出 "{}"）
//   2. 通过 new_req 套上 Authorization
//   3. 检查 HTTP 状态码（NapCat 的 token 鉴权失败会返回 403/401，非 2xx 时直接报错）
//   4. 反序列化业务响应，并通过 BaseRespAccessor 检查 status / retcode
func BaseService(client *http.Client, ReqResp model.BaseInterface) error {
	body, err := json.Marshal(ReqResp.GetReq())
	if err != nil {
		return fmt.Errorf("napcat 请求体序列化失败: %w", err)
	}

	err, req := new_req.NewReq(ReqResp.Name(), body)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("napcat 请求发送失败 url=%s: %w", ReqResp.Name(), err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("napcat 响应读取失败: %w", err)
	}

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("napcat 接口返回 HTTP %d url=%s body=%s",
			resp.StatusCode, ReqResp.Name(), truncate(string(respData), 256))
	}

	if err = json.Unmarshal(respData, ReqResp.GetResp()); err != nil {
		return fmt.Errorf("napcat 响应反序列化失败 url=%s: %w, raw=%s",
			ReqResp.Name(), err, truncate(string(respData), 256))
	}

	return checkBaseStatus(ReqResp.GetResp())
}

// BaseServiceWithForm 给非 NapCat 的 form 接口（如 pixiv 第三方）使用。
func BaseServiceWithForm(client *http.Client, ReqResp model.BaseInterface, form url1.Values) error {
	err, req := new_req.NewReqWithForm(ReqResp.Name(), form)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if err = json.Unmarshal(respData, ReqResp.GetResp()); err != nil {
		return fmt.Errorf("响应反序列化失败: %w, raw=%s", err, truncate(string(respData), 256))
	}
	return nil
}

// checkBaseStatus 根据 NapCat / OneBot11 规范检查业务状态。
//
// 部分接口（pixiv 等第三方）的响应不是标准 OneBot11 形态，
// 这种情况下 type assert 失败，直接放行。
func checkBaseStatus(respPtr interface{}) error {
	accessor, ok := respPtr.(model.BaseRespAccessor)
	if !ok {
		return nil
	}
	base := accessor.GetBaseResp()
	if base == nil || base.IsOK() {
		return nil
	}
	msg := base.Wording
	if msg == "" {
		msg = base.Message
	}
	return fmt.Errorf("napcat 业务返回失败 status=%s retcode=%d msg=%s",
		base.Status, base.RetCode, msg)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
