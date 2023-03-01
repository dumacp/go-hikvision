package client

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/go-hikvision/peoplecounting"
)

// ListenActor actor to listen events
type ListenActor struct {
	*Logger
	context       actor.Context
	countingActor *actor.PID
	entersBefore  int64
	exitsBefore   int64
	timeBefore    *time.Time

	cancel func()

	socket string
}

// NewListen create listen actor
func NewListen(socket string, countingActor *actor.PID) *ListenActor {
	act := &ListenActor{}
	act.countingActor = countingActor
	act.socket = socket
	act.Logger = &Logger{}
	return act
}

// Receive func Receive in actor
func (act *ListenActor) Receive(ctx actor.Context) {
	act.context = ctx
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		act.initLogs()
		act.infoLog.Printf("actor started \"%s\"", ctx.Self().Id)
		contxt, cancel := context.WithCancel(context.TODO())
		act.cancel = cancel
		go act.runListen(contxt)
	case *actor.Stopping:
		act.warnLog.Println("stopped actor")
		if act.cancel != nil {
			act.cancel()
		}
	case *messages.CountingActor:
		act.countingActor = actor.NewPID(msg.Address, msg.ID)
	case *msgListenError:
		act.errLog.Panicln("listen error")
	}
}

type msgListenError struct{}

func parseDateTime(t1 string) (*time.Time, error) {
	b1 := []byte(t1)
	re := regexp.MustCompile("([0-9]{4}-[0-9]{1,2}-[0-9]{1,2}T[0-9]{1,2}:[0-9]{1,2}:[0-9]{1,2})-([0-9]:00)")
	b2 := re.ReplaceAll(b1, []byte("${1}-0${2}"))
	p1, err := time.Parse(time.RFC3339, string(b2))
	if err != nil {
		return nil, err
	}
	return &p1, nil
}

func (act *ListenActor) runListen(ctx context.Context) {
	first := true
	events := peoplecounting.Listen(ctx, act.socket, act.errLog, act.cameralog)
	for v := range events {
		act.buildLog.Printf("listen event: %#v\n", v)
		switch event := v.(type) {
		case *peoplecounting.EventNotificationAlertPeopleConting:
			if strings.Contains(event.PeopleCounting.StatisticalMethods, "timeRange") {
				act.warnLog.Printf("event timeRange, events -> %+v", event.PeopleCounting)
				break
			}
			dateTime, err := parseDateTime(event.DateTime)
			if err != nil {
				act.warnLog.Printf("time event error -> %s", err)
				break
			}
			if act.timeBefore != nil && dateTime.Before(*(act.timeBefore)) {
				act.warnLog.Printf("time event error, events in the past -> new %v, before %v", dateTime, act.timeBefore)
				break
			}
			act.timeBefore = dateTime

			act.cameralog.Printf("%d: listen event: %+v\n", time.Now().UnixNano()/1000_000, event)
			act.cameralog.Printf("%d: listen event: %+v\n", time.Now().UnixNano()/1000_000, event.PeopleCounting)
			if first {
				act.infoLog.Printf("initial event -> %+v", event.PeopleCounting)
				act.infoLog.Printf("initial event -> %+v", event)
				first = false
			}
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
		case *peoplecounting.EventNotificationAlert:
			switch event.EventType {
			case peoplecounting.ScenechangedetectionType:
				act.context.Send(act.countingActor, &messages.Event{Type: messages.TAMPERING, Value: 0})
			case peoplecounting.ShelteralarmType:
				act.context.Send(act.countingActor, &messages.Event{Type: messages.TAMPERING, Value: 0})
			}
		}
	}
	act.context.Send(act.context.Self(), &msgListenError{})
}
