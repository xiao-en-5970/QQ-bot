package new_req

import (
	"bytes"
	"fmt"
	"net/http"
	url1 "net/url"
)

func NewReq(targetURL string, jsonData []byte) (err error, req *http.Request) {
	req, err = http.NewRequest("POST", targetURL, bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Println("创建请求失败:", err)
		return err, nil
	}
	// 设置请求头，指定内容类型为JSON
	req.Header.Set("Content-Type", "application/json")
	return
}

func NewReqWithForm(targetURL string, form url1.Values) (err error, req *http.Request) {
	req, err = http.NewRequest("POST", targetURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		fmt.Println("创建请求失败:", err)
		return err, nil
	}
	// 设置请求头，指定内容类型为JSON
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return
}
