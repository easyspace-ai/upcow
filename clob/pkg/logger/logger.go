package logger

import (
	"log"
)

// 简单的日志实现（避免循环依赖）
var (
	Info  = log.Printf
	Warnf = log.Printf
	Errorf = log.Printf
	Debugf = func(format string, args ...interface{}) {
		// 默认不输出 debug 日志
	}
)

