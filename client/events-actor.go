package client

import (
	"bytes"
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

//EventActor type
type EventActor struct {
	*Logger
	mem1    *memoryGPS
	mem2    *memoryGPS
	puertas map[uint]uint
}

//NewEventActor create EventActor
func NewEventActor() *EventActor {
	event := &EventActor{}
	event.Logger = &Logger{}
	event.mem1 = &memoryGPS{}
	event.mem2 = &memoryGPS{}
	event.puertas = make(map[uint]uint)
	return event
}

type msgEvent struct {
	data []byte
}

//Receive function to Receive actor messages
func (act *EventActor) Receive(ctx actor.Context) {

	switch msg := ctx.Message().(type) {
	case *actor.Started:
		act.initLogs()
		act.infoLog.Printf("actor started \"%s\"", ctx.Self().Id)
	case *messages.Event:
		act.buildLog.Printf("\"%s\" - event -> '%v'\n", ctx.Self().GetId(), msg)
		var event []byte
		switch msg.Type {
		case messages.INPUT:
			event = buildEventPass(ctx, msg, act.mem1, act.mem2, act.puertas, act.Logger)
		case messages.OUTPUT:
			event = buildEventPass(ctx, msg, act.mem1, act.mem2, act.puertas, act.Logger)
		case messages.TAMPERING:
			event = buildEventTampering(ctx, msg, act.mem1, act.mem2, act.puertas, act.Logger)
		}
		ctx.Send(ctx.Parent(), &msgEvent{data: event})
	case *msgGPS:
		// act.buildLog.Printf("\"%s\" - msg: '%q'\n", ctx.Self().GetId(), msg)
		mem := captureGPS(msg.data)
		// act.buildLog.Printf("mem, %v", mem)
		switch mem.typeM {
		case gprmctype:
			act.mem1 = &mem
		case gngnsType:
			act.mem2 = &mem
		}
		// act.buildLog.Printf("memorys 1, %v, %v", act.mem1, act.mem2)
	case *msgDoor:
		act.puertas[msg.id] = msg.value
	case *actor.Stopped:
		act.infoLog.Println("stoped actor")
	}
}

type memoryGPS struct {
	typeM     int
	frame     string
	timestamp int64
}

func captureGPS(gps []byte) memoryGPS {
	// memoryGPRMC := new(memoryGPS)
	// memoryGNSNS := new(memoryGPS)
	// ch1 := make(chan memoryGPS, 2)
	// ch2 := make(chan memoryGPS, 1)
	// go func() {
	// for v := range chGPS {
	memory := memoryGPS{}
	if bytes.Contains(gps, []byte("GPRMC")) {

		memory.typeM = gprmctype
		memory.frame = string(gps)
		memory.timestamp = time.Now().Unix()

	} else if bytes.Contains(gps, []byte("GNGNS")) {
		memory.typeM = gngnsType
		memory.frame = string(gps)
		memory.timestamp = time.Now().Unix()
	}
	return memory
}

func buildEventPass(ctx actor.Context, v *messages.Event, mem1, mem2 *memoryGPS, puerta map[uint]uint, log *Logger) []byte {
	tn := time.Now()

	log.buildLog.Printf("memorys, %v, %v", mem1, mem2)
	contadores := []int64{0, 0}
	if v.Type == messages.INPUT {
		contadores[0] = v.Value
	} else if v.Type == messages.OUTPUT {
		contadores[1] = v.Value
	}
	frame := ""

	// if mem1.timestamp > mem2.timestamp {
	if mem1.timestamp+30 > tn.Unix() {
		frame = mem1.frame
		// }
	} else if mem2.timestamp+30 > tn.Unix() {
		frame = mem2.frame
		// }
	}
	log.buildLog.Printf("frame, %v", frame)

	doorState := uint(0)
	if vm, ok := puerta[gpioPuerta2]; ok {
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
		1,
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

func buildEventTampering(ctx actor.Context, v *messages.Event, mem1, mem2 *memoryGPS, puerta map[uint]uint, log *Logger) []byte {
	tn := time.Now()

	if v.Type != messages.TAMPERING {
		return nil
	}
	frame := ""

	if mem1.timestamp > mem2.timestamp {
		if mem1.timestamp+30 > tn.Unix() {
			frame = mem1.frame
		}
	} else {
		if mem2.timestamp+30 > tn.Unix() {
			frame = mem2.frame
		}
	}

	doorState := uint(0)
	if vm, ok := puerta[gpioPuerta2]; ok {
		doorState = vm
	}

	message := &pubsub.Message{
		Timestamp: float64(time.Now().UnixNano()) / 1000000000,
		Type:      "TAMPERING",
	}

	val := struct {
		Coord string `json:"coord"`
		ID    int    `json:"id"`
		State uint   `json:"state"`
	}{
		frame,
		1,
		doorState,
	}
	message.Value = val

	msg, err := json.Marshal(message)
	if err != nil {
		log.errLog.Println(err)
	}
	log.buildLog.Printf("%s\n", msg)

	return msg
}
