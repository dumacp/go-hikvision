package client

import (
	"encoding/json"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/pubsub"
)

const (
	gprmctype = 0
	gngnsType = 1
)

// EventActor type
type EventActor struct {
	*Logger
	puertas map[uint]uint
}

// NewEventActor create EventActor
func NewEventActor() *EventActor {
	event := &EventActor{}
	event.Logger = &Logger{}
	event.puertas = make(map[uint]uint)
	return event
}

type msgEvent struct {
	data []byte
}
type msgAddEvent struct {
	data []byte
}

// Receive function to Receive actor messages
func (act *EventActor) Receive(ctx actor.Context) {

	switch msg := ctx.Message().(type) {
	case *actor.Started:
		act.initLogs()
		act.infoLog.Printf("actor started \"%s\"", ctx.Self().Id)
	case *messages.Event:
		act.buildLog.Printf("\"%s\" - event -> '%v'\n", ctx.Self().GetId(), msg)
		frame := ""
		res, err := ctx.RequestFuture(ctx.Parent(), &MsgGetGps{}, 180*time.Millisecond).Result()
		if err == nil {
			if datagps, ok := res.(*MsgGPS); ok {
				frame = string(datagps.Data)
			}
		}
		var event []byte
		switch msg.Type {
		case messages.INPUT:

			event = buildEventPass(ctx, msg, frame, act.puertas, act.Logger)
			ctx.Send(ctx.Parent(), &msgEvent{data: event})
		case messages.OUTPUT:
			event = buildEventPass(ctx, msg, frame, act.puertas, act.Logger)
			ctx.Send(ctx.Parent(), &msgEvent{data: event})
		case messages.TAMPERING:
			event = buildEventTampering(ctx, msg, frame, act.puertas, act.Logger)
			ctx.Send(ctx.Parent(), &msgAddEvent{data: event})
		}

	case *MsgDoor:
		act.puertas[msg.ID] = msg.Value
		act.buildLog.Printf("arrived door to events-actor: %v\n", msg)
	case *actor.Stopped:
		act.infoLog.Println("stoped actor")
	}
}

func buildEventPass(ctx actor.Context, v *messages.Event, gps string, puerta map[uint]uint, log *Logger) []byte {
	// tn := time.Now()

	// log.buildLog.Printf("memorys, %v, %v", mem1, mem2)
	contadores := []int64{0, 0}
	if v.Type == messages.INPUT {
		contadores[0] = v.Value
	} else if v.Type == messages.OUTPUT {
		contadores[1] = v.Value
	}
	frame := gps

	id := v.ID
	doorState := uint(0)
	if vm, ok := puerta[uint(id)]; ok {
		doorState = vm
	}

	message := &pubsub.Message{
		Timestamp: float64(time.Now().UnixNano()) / 1000000000,
		Type:      "COUNTERSDOOR",
	}

	val := struct {
		Coord    string  `json:"coord"`
		ID       int     `json:"id"`
		State    uint    `json:"state"`
		Counters []int64 `json:"counters"`
	}{
		frame,
		int(v.ID),
		doorState,
		contadores[0:2],
	}
	message.Value = val

	msg, err := json.Marshal(message)
	if err != nil {
		log.errLog.Println(err)
	}
	log.buildLog.Printf("%s\n", msg)

	return msg
}

func buildEventTampering(ctx actor.Context, v *messages.Event, gps string, puerta map[uint]uint, log *Logger) []byte {
	// tn := time.Now()

	if v.Type != messages.TAMPERING {
		return nil
	}
	frame := gps

	id := v.ID
	doorState := uint(0)
	if vm, ok := puerta[uint(id)]; ok {
		doorState = vm
	}

	message := &pubsub.Message{
		Timestamp: float64(time.Now().UnixNano()) / 1000000000,
		Type:      "TAMPERING",
	}

	val := struct {
		Coord    string  `json:"coord"`
		ID       int     `json:"id"`
		State    uint    `json:"state"`
		Counters []int64 `json:"counters"`
	}{
		frame,
		int(id),
		doorState,
		[]int64{0, 0},
	}

	if id == 0 {
		val.Counters[0] = 1
	} else if id == 1 {
		val.Counters[1] = 1
	}
	message.Value = val

	msg, err := json.Marshal(message)
	if err != nil {
		log.errLog.Println(err)
	}
	log.buildLog.Printf("%s\n", msg)

	return msg
}
