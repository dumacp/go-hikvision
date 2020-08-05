package client

import (
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/go-hikvision/peoplecounting"
)

//ListenActor actor to listen events
type ListenActor struct {
	*Logger
	context       actor.Context
	countingActor *actor.PID
	entersBefore  int
	exitsBefore   int

	quit chan int

	socket string
}

//NewListen create listen actor
func NewListen(countingActor *actor.PID) *ListenActor {
	act := &ListenActor{}
	act.countingActor = countingActor
	act.Logger = &Logger{}
	act.quit = make(chan int, 0)
	return act
}

//Receive func Receive in actor
func (act *ListenActor) Receive(ctx actor.Context) {
	act.context = ctx
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		act.initLogs()
		act.infoLog.Printf("actor started \"%s\"", ctx.Self().Id)
		go act.runListen(act.quit)
	case *actor.Stopped:
		act.warnLog.Println("stopped actor")
		select {
		case act.quit <- 1:
		case <-time.After(3 * time.Second):
		}
	case *messages.CountingActor:
		act.countingActor = actor.NewPID(msg.Address, msg.ID)
	case *msgListenError:
		act.errLog.Panicln("listen error")
	}
}

type msgListenError struct{}

func (act *ListenActor) runListen(quit chan int) {
	events := peoplecounting.Listen(quit, act.socket, act.errLog)
	for v := range events {
		switch event := v.(type) {
		case peoplecounting.EventNotificationAlertPeopleConting:
			enters := event.PeopleCounting.Enter
			if diff := uint32(enters - act.entersBefore); diff > 0 {
				act.context.Send(act.countingActor, &messages.Event{Type: messages.INPUT, Value: diff})
			}
			exits := event.PeopleCounting.Exit
			if diff := uint32(exits - act.exitsBefore); diff > 0 {
				act.context.Send(act.countingActor, &messages.Event{Type: messages.OUTPUT, Value: diff})
			}
		}
	}
	act.context.Send(act.context.Self(), &msgListenError{})
}
