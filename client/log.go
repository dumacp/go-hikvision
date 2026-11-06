package client

import (
	"io/ioutil"
	"log"
	"os"
)

//Logger struct to logger
type Logger struct {
	errLog    *log.Logger
	warnLog   *log.Logger
	infoLog   *log.Logger
	buildLog  *log.Logger
	cameralog *log.Logger

	debug bool
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

//SetLogCamera set logs with INFO level
func (act *Logger) SetLogCamera(info *log.Logger) *Logger {
	act.cameralog = info
	return act
}

//WithDebug apply debug
func (act *Logger) WithDebug() *Logger {
	act.debug = true
	return act
}

// Accesores exportados, para los actores que viven en subpaquetes de client.
//
// Los cinco niveles se inyectan desde main y hasta ahora solo los usaban actores del propio
// paquete, así que alcanzaba con campos privados. Un actor en client/parametros no puede
// llegar a ellos, y darle su propio juego de loggers partiría los prefijos en dos: la mitad
// del binario escribiendo "[ WARN ]" y la otra mitad otra cosa, en el mismo archivo de
// syslog.
func (act *Logger) InitLogs()              { act.initLogs() }
func (act *Logger) LogError() *log.Logger  { return act.errLog }
func (act *Logger) LogWarn() *log.Logger   { return act.warnLog }
func (act *Logger) LogInfo() *log.Logger   { return act.infoLog }
func (act *Logger) LogBuild() *log.Logger  { return act.buildLog }
func (act *Logger) LogCamera() *log.Logger { return act.cameralog }

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
	if act.cameralog == nil {
		act.cameralog = log.New(os.Stderr, "[ INFO ] ", 6)
	}
	if !act.debug {
		act.buildLog.SetOutput(ioutil.Discard)
	}
}
