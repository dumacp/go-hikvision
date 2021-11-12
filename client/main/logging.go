package main

import (
	"io/ioutil"
	"log"
	"log/syslog"
	"os"

	"github.com/dumacp/go-logs/pkg/logs"
)

var (
	warnlog   *log.Logger
	infolog   *log.Logger
	buildlog  *log.Logger
	errlog    *log.Logger
	cameralog *log.Logger
)

func newLogFile(dir, prefixFile string) (*log.Logger, error) {

	logg, err := logs.NewRotate(dir, prefixFile, 1024*1024*2, 20, 0)
	if err != nil {
		return nil, err
	}

	return logg, nil
}

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

func initLogs(debug, logStd, logXml bool) {
	warnlog = newLog(logStd, "[ warn ] ", log.LstdFlags, 4)
	infolog = newLog(logStd, "[ info ] ", log.LstdFlags, 6)
	buildlog = newLog(logStd, "[ build ] ", log.LstdFlags, 7)
	errlog = newLog(logStd, "[ error ] ", log.LstdFlags, 3)
	if logXml {
		cameralog, _ = newLogFile("/SD/logs/", "camera")
	}
	if !debug {
		buildlog.SetOutput(ioutil.Discard)
	}

}
