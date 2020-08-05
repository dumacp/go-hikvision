package client

import (
	"io/ioutil"
	"log"
	"os"
)

//Logger struct to logger
type Logger struct {
	errLog   *log.Logger
	warnLog  *log.Logger
	infoLog  *log.Logger
	buildLog *log.Logger
	debug    bool
}

//SetLogError set logs with ERROR level
func (act *Logger) SetLogError(err *log.Logger) *Logger {
	act.errLog = err
	return act
}

//SetLogWarn set logs with WARN level
func (act *Logger) SetLogWarn(warn *log.Logger) *Logger {
	act.warnLog = warn
	return act
}

//SetLogInfo set logs with INFO level
func (act *Logger) SetLogInfo(info *log.Logger) *Logger {
	act.infoLog = info
	return act
}

//SetLogBuild set logs with INFO level
func (act *Logger) SetLogBuild(info *log.Logger) *Logger {
	act.buildLog = info
	return act
}

//WithDebug apply debug
func (act *Logger) WithDebug() *Logger {
	act.debug = true
	return act
}

func (act *Logger) initLogs() {
	if act.errLog == nil {
		act.errLog = log.New(os.Stderr, "[ ERROR ] :", 3)
	}
	if act.warnLog == nil {
		act.warnLog = log.New(os.Stderr, "[ WARN ] ", 4)
	}
	if act.infoLog == nil {
		act.infoLog = log.New(os.Stderr, "[ INFO ] ", 6)
	}
	if act.buildLog == nil {
		act.buildLog = log.New(os.Stderr, "[ BUILD ] ", 7)
	}
	if !act.debug {
		act.buildLog.SetOutput(ioutil.Discard)
	}
}
