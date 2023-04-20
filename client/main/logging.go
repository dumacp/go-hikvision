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

	logg, err := logs.NewRotate(dir, prefixFile, 1024*1024*2, 20)
	if err != nil {
		return nil, err
	}

	logger := logg.NewLogger("", log.LstdFlags)

	return logger, nil
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
	warnlog = newLog(logStd, "[ warn ] ", 0, 4)
	infolog = newLog(logStd, "[ info ] ", 0, 6)
	buildlog = newLog(logStd, "[ build ] ", 0, 7)
	errlog = newLog(logStd, "[ error ] ", 0, 3)
	if logXml {
		cameralog, _ = newLogFile("/SD/logs/", "camera")
	}
	if !debug {
		buildlog.SetOutput(ioutil.Discard)
	}

}
