package logic

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// napCatFileRef 把一个文件引用规整成「远程 NapCat 也能拿到内容」的形式。
//
// 背景：NapCat 从与 bot 同机（服务器 Linux）迁移到远程主机（家用 Windows）后，
// NapCat 进程和 bot 不在同一台机器，读不到 bot 侧本地磁盘。因此凡是指向 bot 本地的
// 引用（裸本地路径 / file:// 本地 URI）都必须把文件内容内联成 base64://，随 action
// 请求体一起发过去；而 http(s):// 远程地址和已经是 base64:// 的数据原样透传。
//
// 适用：send_group_msg 的图片段、upload_group_file 等所有把「文件」交给 NapCat 的场景。
//
// 注意：base64 会让体积膨胀约 33%，大文件（如 jm 生成的漫画 PDF）会产生较大的 JSON
// 请求体。当前经 frp 隧道 + NapCat 本地环回读取，实测可用；若将来遇到超大文件被
// NapCat 拒收，可改成 bot 侧起临时 http 服务、这里返回可访问 URL 的方案。
func napCatFileRef(ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "http://"),
		strings.HasPrefix(ref, "https://"),
		strings.HasPrefix(ref, "base64://"):
		return ref, nil
	}

	// file:// 本地 URI：剥掉前缀拿到真实路径（file:///tmp/x -> /tmp/x）
	path := strings.TrimPrefix(ref, "file://")

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析文件绝对路径失败 %s: %w", path, err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("读取本地文件失败 %s: %w", abs, err)
	}
	return "base64://" + base64.StdEncoding.EncodeToString(data), nil
}
