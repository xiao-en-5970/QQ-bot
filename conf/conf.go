package conf

import (
	"errors"
	"fmt"
	"github.com/spf13/viper"
)

var Cfg Config

type Server struct {
	Address string `mapstructure:"address"`
}

type Pixiv struct {
	PixivAddress string `mapstructure:"pixiv_address"`
	Size         string `mapstructure:"size"`
}

type Log struct {
	StdOutLogLevel string `mapstructure:"std_out_log_level"`
	LogLevel       string `mapstructure:"log_level"`
}
type Group struct {
	GroupID                 []int64 `mapstructure:"group_id,omitempty"`
	GetGroupHistoryInterval int64   `mapstructure:"get_group_history_interval"`
	UpdateGroupListInterval int64   `mapstructure:"update_group_list_interval"`
	Retry                   int64   `mapstructure:"retry"`
}

type User struct {
	UserID *int64 `mapstructure:"user_id,omitempty"`
}
type Cache struct {
	TmpDir    string `mapstructure:"tmp_dir"`
	PdfTmpDir string `mapstructure:"pdf_tmp_dir"`
	MaxSize   int64  `mapstructure:"max_size"`
}
type Config struct {
	Log    Log    `mapstructure:"log"`
	Server Server `mapstructure:"server"`
	Pixiv  Pixiv  `mapstructure:"pixiv"`
	Group  Group  `mapstructure:"group"`
	User   User   `mapstructure:"user"`
	Cache  Cache  `mapstructure:"cache"`
}

func Init() (err error) {
	// 创建一个新的 viper 实例
	viper.SetConfigName("test") // 配置文件名（不带扩展名）
	viper.SetConfigType("yaml") // 配置文件类型
	viper.AddConfigPath(".")    // 配置文件路径（当前目录）
	// 如果配置文件不在当前目录，可以添加更多路径
	// viper.AddConfigPath("/etc/myapp/")
	// viper.AddConfigPath("$HOME/.myapp")

	// 读取配置文件
	if err = viper.ReadInConfig(); err != nil {
		var configFileNotFoundError viper.ConfigFileNotFoundError
		if errors.As(err, &configFileNotFoundError) {
			return errors.New(fmt.Sprintf("配置文件未找到: %v", err))
		}
	}

	// 将配置文件内容映射到 Config 结构体

	if err = viper.Unmarshal(&Cfg); err != nil {

		return errors.New(fmt.Sprintf("无法解析配置文件: %v", err))
	}

	return nil
}
