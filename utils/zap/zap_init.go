package zap

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"os"
	"path/filepath"
	"qq_bot/conf"
)

var Logger *zap.SugaredLogger
var LogFile *os.File

// Init 初始化 zap 日志器，输出到 文件 + 标准输出。
//
// 文件路径取自 conf.Cfg.Log.LogFile（默认走 os.TempDir 下的 qq-bot/logs/qq-bot.log），
// 上层目录不存在会自动 mkdir。Docker 部署时建议宿主机 /qq-bot-server/cache:/tmp/qq-bot 整段挂出来。
func Init() {
	logPath := conf.Cfg.Log.LogFile
	if logPath == "" {
		logPath = filepath.Join(os.TempDir(), "qq-bot", "logs", "qq-bot.log")
	}
	if dir := filepath.Dir(logPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(err)
		}
	}

	var err error
	LogFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		panic(err)
	}

	fileLevel := parseLevel(conf.Cfg.Log.LogLevel)

	fileEncoderConfig := zap.NewProductionEncoderConfig()
	fileEncoderConfig.TimeKey = "timestamp"
	fileEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	fileEncoder := zapcore.NewJSONEncoder(fileEncoderConfig)
	fileCore := zapcore.NewCore(
		fileEncoder,
		zapcore.AddSync(LogFile),
		fileLevel,
	)

	consoleEncoderConfig := zap.NewProductionEncoderConfig()
	consoleEncoderConfig.TimeKey = "timestamp"
	consoleEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	consoleEncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder

	consoleLevel := parseLevel(conf.Cfg.Log.StdOutLogLevel)
	consoleEncoder := zapcore.NewConsoleEncoder(consoleEncoderConfig)
	consoleCore := zapcore.NewCore(
		consoleEncoder,
		zapcore.AddSync(os.Stdout),
		consoleLevel,
	)

	core := zapcore.NewTee(fileCore, consoleCore)
	l := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	Logger = l.Sugar()
	Logger.Infof("日志初始化成功，日志文件: %s", logPath)
}

func parseLevel(s string) zapcore.Level {
	switch s {
	case "debug":
		return zap.DebugLevel
	case "info":
		return zap.InfoLevel
	case "warn":
		return zap.WarnLevel
	case "error":
		return zap.ErrorLevel
	case "fatal":
		return zap.FatalLevel
	default:
		return zap.WarnLevel
	}
}
