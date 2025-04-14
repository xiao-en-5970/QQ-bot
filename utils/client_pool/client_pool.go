package client_pool

import (
	"net"
	"net/http"
	"time"
)

func NewClientPool() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        256, // 总连接池大小
			MaxIdleConnsPerHost: 10,  // 每个host保持的空闲连接
			IdleConnTimeout:     90 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second, // TCP保活
			}).DialContext,
		},
		Timeout: 120 * time.Second, // 请求超时
	}
}
