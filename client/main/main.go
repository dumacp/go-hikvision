package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/persistence"
	"github.com/dumacp/go-hikvision/client"
	"github.com/dumacp/go-hikvision/client/messages"
	"golang.org/x/exp/errors/fmt"
)

const (
	showVersion = "1.0.32"
)

var debug bool
var logStd bool
var socket string
var pathdb string
var version bool
var logXML bool

var encryptCreds bool

var videoDir string
var videoPreRoll time.Duration
var videoDuration time.Duration
var videoQueue int

var ntpServer string
var ntpPort int
var ntpInterval time.Duration
var ntpTimeZone string
var ntpDriftMax time.Duration
var cameraCheckInterval time.Duration

var isZeroOpenState zeroFlags
var enableCountWithCloseDoor closeFlags
var cameras cameraFlags

func init() {
	flag.BoolVar(&debug, "debug", false, "debug enable")
	flag.BoolVar(&logStd, "logStd", false, "log in stderr")
	flag.StringVar(&socket, "socket", ":8088", "socket to listen events")
	flag.StringVar(&pathdb, "pathdb", "/SD/boltdbs/countingdb", "socket to listen events")
	flag.BoolVar(&version, "version", false, "show version")
	flag.BoolVar(&logXML, "logxml", false, "logging XML in file")
	flag.BoolVar(&encryptCreds, "encryptCredentials", false,
		"read user and password from stdin (one per line) and print the "+envCredentials+" value")
	flag.Var(&isZeroOpenState, "zeroOpenState", "Is Zero the open state?")
	flag.Var(&enableCountWithCloseDoor, "countWithCloseDoor", "enable count with close door?")
	flag.Var(&cameras, "camera", "camera IP of a door; the n-th occurrence is door n-1")
	flag.StringVar(&videoDir, "videoDir", "",
		"directory to store the video clip of every pass; empty disables the extraction")
	// 10s de pre-roll medidos contra tráfico real: el dateTime del evento llega entre
	// 2 y 7 segundos DESPUÉS del cruce físico, así que con 5s el cruce quedaba en el
	// borde del clip o afuera. Con 10s/16s los tres cruces de una fila entraron en los
	// segundos 7 a 11 de la ventana.
	flag.DurationVar(&videoPreRoll, "videoPreRoll", 10*time.Second,
		"how much video to keep before the event, so the crossing itself is in the clip")
	flag.DurationVar(&videoDuration, "videoDuration", 16*time.Second, "clip duration")
	flag.IntVar(&videoQueue, "videoQueue", 500,
		"maximum pending extractions; playback runs in real time, so a burst queues up")
	flag.StringVar(&ntpServer, "ntpServer", "",
		"NTP server the cameras must use; empty disables the time and NTP maintenance")
	flag.IntVar(&ntpPort, "ntpPort", 123, "NTP server port")
	// El intervalo va en minutos en ISAPI. Un equipo verificado venía con 1500, que son
	// 25 horas entre sincronizaciones: NTP configurado y aun así derivando.
	flag.DurationVar(&ntpInterval, "ntpInterval", 60*time.Minute,
		"how often the camera synchronizes; ISAPI stores it in minutes")
	// POSIX invierte el signo: CST+5:00:00 es UTC-5. Escribirlo como -5:00:00 deja la
	// cámara diez horas corrida.
	flag.StringVar(&ntpTimeZone, "timeZone", "CST+5:00:00",
		"POSIX time zone for the cameras; the sign is inverted, CST+5:00:00 means UTC-5")
	flag.DurationVar(&ntpDriftMax, "timeDriftMax", 10*time.Second,
		"drift tolerated before reporting a camera clock as off")
	flag.DurationVar(&cameraCheckInterval, "cameraCheckInterval", 30*time.Minute,
		"how often the camera time and NTP configuration is checked")
}

func main() {

	flag.Parse()

	if version {
		fmt.Printf("version: %s\n", showVersion)
		os.Exit(2)
	}

	if encryptCreds {
		os.Exit(printEncryptedCredentials())
	}

	initLogs(debug, logStd, logXML)

	// Credentials are only needed to talk to the camera, so a missing or unreadable
	// value must not stop the counting: warn and keep going.
	camUser, camPass, err := credentialsFromEnv()
	switch {
	case err != nil:
		warnlog.Printf("%s: %s", envCredentials, err)
	case len(camUser) == 0:
		infolog.Printf("%s not set, camera dialogue disabled", envCredentials)
	default:
		infolog.Printf("camera credentials loaded for user %q", camUser)
	}

	// peoplecounting.Listen(socket, errlog)

	provider, err := newProvider(pathdb, 10)
	if err != nil {
		log.Fatalln(err)
	}

	rootContext := actor.NewActorSystem().Root

	if len(isZeroOpenState) <= 0 {
		isZeroOpenState = []bool{false}
	}
	if len(enableCountWithCloseDoor) <= 0 {
		enableCountWithCloseDoor = []bool{false}
	}

	fmt.Printf("zeroOpenState: %v\n", isZeroOpenState)
	fmt.Printf("countWithCloseDoor: %v\n", enableCountWithCloseDoor)
	if len(cameras) > 0 {
		fmt.Printf("cameras (index = door id): %v\n", cameras)
	} else {
		fmt.Printf("cameras: not configured, using the legacy rule (%s -> door 1, rest -> door 0)\n",
			client.LegacyBackDoorIP())
	}

	counting := client.NewCountingActor()
	for i, v := range isZeroOpenState {
		counting.SetZeroOpenState(i, v)
	}
	for i, v := range enableCountWithCloseDoor {
		counting.SetCountCloseDoor(i, v)
	}
	counting.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).
		SetLogBuild(buildlog)
	if debug {
		counting.WithDebug()
	}

	// La extracción de video necesita las tres cosas: dónde guardar, a qué cámara
	// preguntarle y con qué credenciales. Si falta alguna, queda deshabilitada y el
	// conteo sigue igual.
	switch {
	case len(videoDir) == 0:
		infolog.Println("video extraction disabled (-videoDir not set)")
	case len(cameras) == 0:
		warnlog.Println("video extraction disabled: -videoDir is set but there is no -camera")
	case len(camUser) == 0:
		warnlog.Printf("video extraction disabled: -videoDir is set but %s is not usable", envCredentials)
	default:
		vid := client.NewVideoActor(cameras, camUser, camPass, videoDir,
			videoPreRoll, videoDuration, videoQueue)
		vid.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).SetLogBuild(buildlog)
		if debug {
			vid.WithDebug()
		}
		counting.SetVideoProps(actor.PropsFromProducer(func() actor.Actor { return vid }))
		infolog.Printf("video extraction enabled: dir=%q preRoll=%v duration=%v queue=%d",
			videoDir, videoPreRoll, videoDuration, videoQueue)
	}

	// El mantenimiento de hora y NTP exige las mismas tres cosas que el video: a qué
	// cámara preguntarle, con qué credenciales, y acá además el servidor deseado.
	switch {
	case len(ntpServer) == 0:
		infolog.Println("camera time maintenance disabled (-ntpServer not set)")
	case len(cameras) == 0:
		warnlog.Println("camera time maintenance disabled: -ntpServer is set but there is no -camera")
	case len(camUser) == 0:
		warnlog.Printf("camera time maintenance disabled: -ntpServer is set but %s is not usable",
			envCredentials)
	default:
		cam := client.NewCameraActor(cameras, camUser, camPass, client.NTPConfig{
			Server:   ntpServer,
			Port:     ntpPort,
			Interval: ntpInterval,
			TimeZone: ntpTimeZone,
			DriftMax: ntpDriftMax,
		}, cameraCheckInterval)
		cam.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).SetLogBuild(buildlog)
		if debug {
			cam.WithDebug()
		}
		counting.SetCameraProps(actor.PropsFromProducer(func() actor.Actor { return cam }))
		infolog.Printf("camera time maintenance enabled: ntp=%s:%d every %v, zone=%q, driftMax=%v, check=%v",
			ntpServer, ntpPort, ntpInterval, ntpTimeZone, ntpDriftMax, cameraCheckInterval)
	}

	propsCounting := actor.PropsFromProducer(func() actor.Actor { return counting }, actor.WithReceiverMiddleware(persistence.Using(provider)))
	pidCounting, err := rootContext.SpawnNamed(propsCounting, "counting")
	if err != nil {
		time.Sleep(3 * time.Second)
		errlog.Panicln(err)
	}

	listenner := client.NewListen(socket, pidCounting)
	listenner.SetCameras(cameras)
	listenner.SetLogError(errlog).SetLogWarn(warnlog).
		SetLogInfo(infolog).SetLogBuild(buildlog).SetLogCamera(cameralog)

	if debug {
		listenner.WithDebug()
	}

	propsListen := actor.PropsFromFunc(listenner.Receive)
	pidListen, err := rootContext.SpawnNamed(propsListen, "listenner")
	if err != nil {
		time.Sleep(3 * time.Second)
		errlog.Panicln(err)
	}

	time.Sleep(1 * time.Second)

	rootContext.Send(pidListen, &messages.CountingActor{
		Address: pidCounting.Address,
		ID:      pidCounting.Id})

	time.Sleep(3 * time.Second)

	rootContext.Send(pidCounting, &client.MsgSendRegisters{})

	infolog.Printf("back camera counter START --  version: %s\n", showVersion)

	go func() {
		t1 := time.NewTicker(45 * time.Second)
		defer t1.Stop()
		for range t1.C {
			rootContext.Send(pidCounting, &client.MsgSendRegisters{})
		}
	}()

	finish := make(chan os.Signal, 1)
	signal.Notify(finish, syscall.SIGINT)
	signal.Notify(finish, syscall.SIGTERM)
	<-finish
}
