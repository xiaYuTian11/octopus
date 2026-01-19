package log

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var Logger *zap.SugaredLogger
var atomicLevel = zap.NewAtomicLevelAt(zap.InfoLevel)
var consoleEncoder = zapcore.EncoderConfig{
	TimeKey:       "time",
	LevelKey:      "level",
	MessageKey:    "msg",
	CallerKey:     "caller",
	StacktraceKey: "stacktrace",
	EncodeLevel:   zapcore.CapitalLevelEncoder,
	EncodeTime:    zapcore.RFC3339TimeEncoder,
	EncodeCaller:  zapcore.ShortCallerEncoder,
}

func init() {
	// 创建文件输出
	fileWriter := &lumberjack.Logger{
		Filename:   "logs/octopus.log",
		MaxSize:    100, // MB
		MaxBackups: 7,
		MaxAge:     30, // days
		Compress:   true,
	}

	// 创建多输出：同时输出到控制台和文件
	multiWriter := zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(os.Stdout),
		zapcore.AddSync(fileWriter),
	)

	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(consoleEncoder),
		multiWriter,
		atomicLevel,
	)
	opts := []zap.Option{
		zap.AddCaller(),
		zap.AddCallerSkip(1),
		zap.AddStacktrace(zap.ErrorLevel),
	}
	Logger = zap.New(core, opts...).Sugar()
}

func SetLevel(level string) {
	var lvl zapcore.Level
	err := lvl.UnmarshalText([]byte(level))
	if err != nil {
		return
	}
	atomicLevel.SetLevel(lvl)
}

func Infof(template string, args ...interface{}) {
	Logger.Infof(template, args...)
}

func Errorf(template string, args ...interface{}) {
	Logger.Errorf(template, args...)
}

func Warnf(template string, args ...interface{}) {
	Logger.Warnf(template, args...)
}

func Debugf(template string, args ...interface{}) {
	Logger.Debugf(template, args...)
}
