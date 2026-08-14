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
	showVersion = "1.0.30"
)

var debug bool
var logStd bool
var socket string
var pathdb string
var version bool
var logXML bool

var encryptCreds bool

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
	camUser, _, err := credentialsFromEnv()
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
