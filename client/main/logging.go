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

func newLog(debug bool, prefix string, flags int, priority int) *log.Logger {
	if debug {
		return log.New(os.Stderr, prefix, flags)
	}

	logg, err := syslog.NewLogger(syslog.Priority(priority), flags)
	if err != nil {
		logg = log.New(os.Stderr, prefix, flags)
	}
	return logg
}

func initLogs(debug bool) {
	warnlog = newLog(debug, "[ warn ] ", log.LstdFlags, 4)
	infolog = newLog(debug, "[ info ] ", log.LstdFlags, 6)
	buildlog = newLog(debug, "[ build ] ", log.LstdFlags, 7)
	errlog = newLog(debug, "[ error ] ", log.LstdFlags, 3)
	if !debug {
		buildlog.SetOutput(ioutil.Discard)
	}
}
