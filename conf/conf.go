package conf

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

var Cfg Config

// Server 对应 NapCat 的 HTTP 接口配置
//
// Address  : NapCat HTTP 服务的根地址，必须以 / 结尾。例如 http://localhost:3000/
// AccessToken : NapCat 在 onebot11 配置里 token 字段，没设的话留空
type Server struct {
	Address     string `mapstructure:"address"`
	AccessToken string `mapstructure:"access_token,omitempty"`
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
	TmpDir        string `mapstructure:"tmp_dir"`
	PdfTmpDir     string `mapstructure:"pdf_tmp_dir"`
	MaxSize       int64  `mapstructure:"max_size"`
	ClearInterval int64  `mapstructure:"clear_interval"`
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
	viper.SetConfigName("test")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	if err = viper.ReadInConfig(); err != nil {
		var configFileNotFoundError viper.ConfigFileNotFoundError
		if errors.As(err, &configFileNotFoundError) {
			return errors.New(fmt.Sprintf("配置文件未找到: %v", err))
		}
	}

	if err = viper.Unmarshal(&Cfg); err != nil {

		return errors.New(fmt.Sprintf("无法解析配置文件: %v", err))
	}

	if Cfg.Server.Address != "" && !strings.HasSuffix(Cfg.Server.Address, "/") {
		Cfg.Server.Address = Cfg.Server.Address + "/"
	}

	return nil
}
