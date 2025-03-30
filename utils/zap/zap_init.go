package zap

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"os"
	"qq_bot/conf"
)

var Logger *zap.SugaredLogger
var LogFile *os.File

func Init() {
	// 打开日志文件
	LogFile, err := os.OpenFile("zap.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	var level zapcore.Level
	switch conf.Cfg.Log.LogLevel {
	case "debug":
		level = zap.DebugLevel
	case "info":
		level = zap.InfoLevel
	case "warn":
		level = zap.WarnLevel
	case "error":
		level = zap.ErrorLevel
	case "fatal":
		level = zap.FatalLevel
	default:
		level = zap.WarnLevel
	}
	// 创建一个写入文件的核心
	fileEncoderConfig := zap.NewProductionEncoderConfig()
	fileEncoderConfig.TimeKey = "timestamp"
	fileEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	fileEncoder := zapcore.NewJSONEncoder(fileEncoderConfig)
	fileCore := zapcore.NewCore(
		fileEncoder,
		zapcore.AddSync(LogFile),
		level, // 设置日志级别
	)

	// 创建一个写入标准输出的核心
	consoleEncoderConfig := zap.NewProductionEncoderConfig()
	consoleEncoderConfig.TimeKey = "timestamp"
	consoleEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	consoleEncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder // 彩色输出

	switch conf.Cfg.Log.StdOutLogLevel {
	case "debug":
		level = zap.DebugLevel
	case "info":
		level = zap.InfoLevel
	case "warn":
		level = zap.WarnLevel
	case "error":
		level = zap.ErrorLevel
	case "fatal":
		level = zap.FatalLevel
	default:
		level = zap.WarnLevel
	}
	consoleEncoder := zapcore.NewConsoleEncoder(consoleEncoderConfig)
	consoleCore := zapcore.NewCore(
		consoleEncoder,
		zapcore.AddSync(os.Stdout),
		level, // 设置日志级别
	)

	// 组合多个核心，实现同时输出到文件和标准输出
	core := zapcore.NewTee(fileCore, consoleCore)
	// 创建 logger
	l := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	Logger = l.Sugar()
	Logger.Infoln("日志初始化成功")
}
