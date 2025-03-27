package jm

import (
	"fmt"
	"os/exec"
)

func Jmcomic(num int64) (err error) {
	//fmt.Println(os.Getwd())
	cmd := exec.Command("./package/jmcomic.exe ", fmt.Sprint(num), "--option=./package/jmoption/opt.yml")

	// 运行命令并获取输出结果
	output, err := cmd.CombinedOutput()
	fmt.Println(string(output))
	if err != nil {
		fmt.Printf("执行命令时发生错误: %v", err)
		return
	}

	// 将输出结果转换为字符串并打印
	fmt.Printf("命令输出结果:%s", string(output))
	return nil
}
