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
	entersBefore  int64
	exitsBefore   int64

	quit chan int

	socket string
}

//NewListen create listen actor
func NewListen(socket string, countingActor *actor.PID) *ListenActor {
	act := &ListenActor{}
	act.countingActor = countingActor
	act.socket = socket
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
		// act.buildLog.Printf("listen event: %v\n", v)
		switch event := v.(type) {
		case peoplecounting.EventNotificationAlertPeopleConting:
			enters := event.PeopleCounting.Enter
			if diff := enters - act.entersBefore; diff > 0 {
				act.context.Send(act.countingActor, &messages.Event{Type: messages.INPUT, Value: enters})
			}
			act.entersBefore = enters
			exits := event.PeopleCounting.Exit
			if diff := exits - act.exitsBefore; diff > 0 {
				act.context.Send(act.countingActor, &messages.Event{Type: messages.OUTPUT, Value: exits})
			}
			act.exitsBefore = exits
		case peoplecounting.EventNotificationAlert:
			switch event.EventType {
			case peoplecounting.ScenechangedetectionType:
				act.context.Send(act.countingActor, &messages.Event{Type: messages.SCENE, Value: 0})
			}
		}
	}
	act.context.Send(act.context.Self(), &msgListenError{})
}
