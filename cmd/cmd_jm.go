package cmd

import "qq_bot/utils/jm"

func CmdJm(num int64) (err error) {
	return jm.Jmcomic(num)
}
