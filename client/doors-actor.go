package client

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/dumacp/go-doors/doorsmsg"
	"github.com/dumacp/go-logs/pkg/logs"
	psub "github.com/dumacp/pubsub"
)

// Actor actor to listen events
type DoorsActor struct {
	ctx actor.Context
}

func NewDoorsActor() actor.Actor {
	a := &DoorsActor{}
	return a
}

func parseDoorsEvents(msg []byte) (interface{}, error) {

	type ValueDoor struct {
		Coord string `json:"coord"`
		ID    int    `json:"id"`
		State int    `json:"state"`
	}

	val := &ValueDoor{}

	event := new(psub.Message)
	event.Value = val

	if err := json.Unmarshal(msg, event); err != nil {
		return nil, err
	}

	result := new(MsgDoor)

	resVal, ok := event.Value.(*ValueDoor)
	if !ok {
		return nil, fmt.Errorf("empty value in door message: %v", event)
	}

	result.ID = uint(resVal.ID)
	result.Value = uint(resVal.State)

	return result, nil
}

// Receive func Receive in actor
func (a *DoorsActor) Receive(ctx actor.Context) {
	logs.LogBuild.Printf("Message arrived in doorsActor: %T, %s",
		ctx.Message(), ctx.Sender())

	a.ctx = ctx
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		logs.LogInfo.Printf("started \"%s\", %v", ctx.Self().GetId(), ctx.Self())
		if err := SubscribeFun("EVENTS/doors", ctx.Self(), parseDoorsEvents); err != nil {
			time.Sleep(3 * time.Second)
			logs.LogError.Panic(err)
		}
		if err := SubscribeFun("camera/doors", ctx.Self(), parseDoorsEvents); err != nil {
			time.Sleep(3 * time.Second)
			logs.LogError.Panic(err)
		}
		req := doorsmsg.DoorsStateRequest{
			TopicResponse: "camera/doors",
		}
		data, err := json.Marshal(req)
		if err != nil {
			logs.LogError.Println(err)
		} else {
			Publish("DOORS", data)
		}
	case *actor.Stopping:
		logs.LogWarn.Printf("\"%s\" - Stopped actor, reason -> %v", ctx.Self(), msg)
	case *actor.Restarting:
		logs.LogWarn.Printf("\"%s\" - Restarting actor, reason -> %v", ctx.Self(), msg)
	case *actor.Terminated:
		logs.LogWarn.Printf("\"%s\" - Terminated actor, reason -> %v", ctx.Self(), msg)
	case *MsgDoor:
		if ctx.Parent() != nil {
			ctx.Send(ctx.Parent(), msg)
		}
	}
}
