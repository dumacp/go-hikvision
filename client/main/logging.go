package main

import (
	"io/ioutil"
	"log"
	"log/syslog"
	"os"
)

var (
	warnlog  *log.Logger
	infolog  *log.Logger
	buildlog *log.Logger
	errlog   *log.Logger
)

func newLog(logStd bool, prefix string, flags int, priority int) *log.Logger {
	if logStd {
		return log.New(os.Stderr, prefix, flags)
	}

	logg, err := syslog.NewLogger(syslog.Priority(priority), flags)
	if err != nil {
		logg = log.New(os.Stderr, prefix, flags)
	}
	return logg
}

func initLogs(debug, logStd bool) {
	warnlog = newLog(logStd, "[ warn ] ", log.LstdFlags, 4)
	infolog = newLog(logStd, "[ info ] ", log.LstdFlags, 6)
	buildlog = newLog(logStd, "[ build ] ", log.LstdFlags, 7)
	errlog = newLog(logStd, "[ error ] ", log.LstdFlags, 3)
	if !debug {
		buildlog.SetOutput(ioutil.Discard)
	}
}
