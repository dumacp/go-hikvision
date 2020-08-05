package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/AsynkronIT/protoactor-go/persistence"
	"github.com/dumacp/go-hikvision/client"
	"github.com/dumacp/go-hikvision/client/messages"
)

const (
	socket = "0.0.0.0:8888"
)

var debug bool

func init() {
	flag.BoolVar(&debug, "debug", false, "debug enable")
}

func main() {

	flag.Parse()
	initLogs(debug)

	// peoplecounting.Listen(socket, errlog)

	provider, err := newProvider(4)
	if err != nil {
		log.Fatalln(err)
	}

	rootContext := actor.EmptyRootContext

	counting := client.NewCountingActor()
	counting.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).SetLogBuild(buildlog)
	if debug {
		counting.WithDebug()
	}

	propsCounting := actor.PropsFromProducer(func() actor.Actor { return counting }).WithReceiverMiddleware(persistence.Using(provider))
	pidCounting, err := rootContext.SpawnNamed(propsCounting, "counting")
	if err != nil {
		errlog.Println(err)
	}

	listenner := client.NewListen(pidCounting)
	listenner.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog)
	if debug {
		listenner.WithDebug()
	}

	propsListen := actor.PropsFromFunc(listenner.Receive)
	pidListen, err := rootContext.SpawnNamed(propsListen, "listenner")
	if err != nil {
		errlog.Println(err)
	}

	rootContext.Send(pidListen, &messages.CountingActor{
		Address: pidCounting.Address,
		ID:      pidCounting.Id})

	time.Sleep(10 * time.Second)

	rootContext.PoisonFuture(pidListen).Wait()
	pidListen, err = rootContext.SpawnNamed(propsListen, "listenner")
	if err != nil {
		errlog.Println(err)
	}

	finish := make(chan os.Signal, 1)
	signal.Notify(finish, syscall.SIGINT)
	signal.Notify(finish, syscall.SIGTERM)
	<-finish
}