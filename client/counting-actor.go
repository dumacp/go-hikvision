package client

import (
	"encoding/json"
	"log"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/AsynkronIT/protoactor-go/persistence"
	"github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/go-logs/pkg/logs"
	"github.com/dumacp/pubsub"
)

// CountingActor struct
type CountingActor struct {
	persistence.Mixin
	*Logger
	// flagRecovering bool
	openState      map[int]uint
	countCloseDoor map[int]bool
	puertas        map[uint]uint
	inputsmap      map[int32]int64
	outputsmap     map[int32]int64
	rawInputsmap   map[int32]int64
	rawOutputsmap  map[int32]int64
	allInputsmap   map[int32]int64
	allOutputsmap  map[int32]int64
	tamperingmap   map[int32]int64

	// pubsub *actor.PID
	doors  *actor.PID
	events *actor.PID
	ping   *actor.PID
	gps    *actor.PID
}

const (
	backdoorID = 1
)

// NewCountingActor create CountingActor
func NewCountingActor() *CountingActor {
	count := &CountingActor{
		inputsmap:     make(map[int32]int64),
		outputsmap:    make(map[int32]int64),
		rawInputsmap:  make(map[int32]int64),
		rawOutputsmap: make(map[int32]int64),
		allInputsmap:  make(map[int32]int64),
		allOutputsmap: make(map[int32]int64),
		tamperingmap:  make(map[int32]int64),
	}
	count.Logger = &Logger{}
	count.puertas = make(map[uint]uint)
	count.countCloseDoor = make(map[int]bool)
	count.openState = make(map[int]uint)
	return count
}

// SetZeroOpenState set the open state in gpio door
func (a *CountingActor) SetZeroOpenState(id int, state bool) {
	if state {
		a.openState[id] = 0
	} else {
		a.openState[id] = 1
	}
}

// SetZeroOpenState set the open state in gpio door
func (a *CountingActor) SetCountCloseDoor(id int, state bool) {
	if state {
		a.countCloseDoor[id] = true
	} else {
		a.countCloseDoor[id] = false
	}
}

type MsgSendRegisters struct{}

// Receive function to receive message in actor
func (a *CountingActor) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		a.initLogs()
		a.infoLog.Printf("actor started \"%s\"", ctx.Self().Id)

		InitPubSub(ctx.ActorSystem().Root)
		time.Sleep(3 * time.Second)

		events := NewEventActor()
		events.SetLogError(a.errLog).
			SetLogWarn(a.warnLog).
			SetLogInfo(a.infoLog).
			SetLogBuild(a.buildLog)
		if a.debug {
			events.WithDebug()
		}
		props2 := actor.PropsFromProducer(func() actor.Actor { return events })
		pid2, err := ctx.SpawnNamed(props2, "events")
		if err != nil {
			a.errLog.Panicln(err)
		}
		a.events = pid2

		props3 := actor.PropsFromProducer(func() actor.Actor { return NewDoorsActor() })
		pid3, err := ctx.SpawnNamed(props3, "doors")
		if err != nil {
			a.errLog.Panicln(err)
		}
		a.doors = pid3

		props4 := actor.PropsFromProducer(func() actor.Actor { return &PingActor{} })
		pid4, err := ctx.SpawnNamed(props4, "ping")
		if err != nil {
			a.errLog.Panicln(err)
		}
		a.ping = pid4

		props5 := actor.PropsFromProducer(func() actor.Actor { return NewGPSActor() })
		pid5, err := ctx.SpawnNamed(props5, "gps")
		if err != nil {
			a.errLog.Panicln(err)
		}
		a.gps = pid5

	case *persistence.RequestSnapshot:
		a.buildLog.Printf("snapshot internal state: inputs -> '%v', outputs -> '%v', rawInputs -> %v, rawOutpts -> %v, allInputs -> %v, allOutpts -> %v\n",
			a.inputsmap, a.outputsmap, a.rawInputsmap, a.rawOutputsmap, a.allInputsmap, a.allOutputsmap)
		snap := &messages.Snapshot{
			InputsMap:     make(map[int32]int64),
			OutputsMap:    make(map[int32]int64),
			RawInputsMap:  make(map[int32]int64),
			RawOutputsMap: make(map[int32]int64),
			AllInputsMap:  make(map[int32]int64),
			AllOutputsMap: make(map[int32]int64),
		}
		for k, v := range a.inputsmap {
			snap.InputsMap[k] = v
		}
		for k, v := range a.outputsmap {
			snap.OutputsMap[k] = v
		}
		for k, v := range a.rawInputsmap {
			snap.RawInputsMap[k] = v
		}
		for k, v := range a.rawOutputsmap {
			snap.RawOutputsMap[k] = v
		}
		for k, v := range a.allInputsmap {
			snap.AllInputsMap[k] = v
		}
		for k, v := range a.allOutputsmap {
			snap.AllOutputsMap[k] = v
		}

		a.PersistSnapshot(snap)
		ctx.Send(ctx.Self(), &MsgSendRegisters{})

	case *MsgSendRegisters:

		if verifySum(a.outputsmap) <= 0 && verifySum(a.inputsmap) <= 0 {
			break
		}
		data, err := registersMap(a.inputsmap, a.outputsmap, make(map[int32]int64), a.tamperingmap)
		if err != nil {
			logs.LogWarn.Println(err)
			break
		}
		log.Printf("data: %q", data)
		a.buildLog.Printf("data: %q", data)
		Publish(topicCounter, data)

	case *messages.Snapshot:
		if inputsMap := msg.GetInputsMap(); inputsMap != nil {
			a.inputsmap = inputsMap
		}

		if outputsMap := msg.GetOutputsMap(); outputsMap != nil {
			a.outputsmap = outputsMap
		}

		if rawInputsMap := msg.GetRawInputsMap(); rawInputsMap != nil {
			a.rawInputsmap = rawInputsMap
		}

		if rawOutputsMap := msg.GetRawOutputsMap(); rawOutputsMap != nil {
			a.rawOutputsmap = rawOutputsMap
		}

		if allInputsMap := msg.GetAllInputsMap(); allInputsMap != nil {
			a.allInputsmap = allInputsMap
		}

		if allOutputsMap := msg.GetAllOutputsMap(); allOutputsMap != nil {
			a.allOutputsmap = allOutputsMap
		}

		if tamperingMap := msg.GetTamperingMap(); tamperingMap != nil {
			a.tamperingmap = tamperingMap
		}

		//backwards compatibility
		if msg.GetInputs() > 0 {
			a.inputsmap[1] = msg.GetInputs()
		}
		if msg.GetOutputs() > 0 {
			a.outputsmap[1] = msg.GetOutputs()
		}
		if msg.GetAllInputs() > 0 {
			a.allInputsmap[1] = msg.GetAllInputs()
		}
		if msg.GetAllOutputs() > 0 {
			a.allOutputsmap[1] = msg.GetAllOutputs()
		}
		if msg.GetRawInputs() > 0 {
			a.rawInputsmap[1] = msg.GetRawInputs()
		}
		if msg.GetRawOutputs() > 0 {
			a.rawOutputsmap[1] = msg.GetRawOutputs()
		}

		a.infoLog.Printf("recovered from snapshot, internal state changed to:\n\tinputs -> '%v', outputs -> '%v', rawInputs -> %v, rawOutpts -> %v, allInputs -> %v, allOutpts -> %v\n",
			a.inputsmap, a.outputsmap, a.rawInputsmap, a.rawOutputsmap, a.allInputsmap, a.allOutputsmap)
		// ctx.Send(ctx.Self(), &MsgSendRegisters{})
	case *persistence.ReplayComplete:
		a.infoLog.Printf("replay completed, internal state changed to:\n\tinputs -> '%v', outputs -> '%v', rawInputs -> %v, rawOutpts -> %v, allInputs -> %v, allOutpts -> %v\n",
			a.inputsmap, a.outputsmap, a.rawInputsmap, a.rawOutputsmap, a.allInputsmap, a.allOutputsmap)
		// a.PersistSnapshot(snap)
		ctx.Send(ctx.Self(), &MsgSendRegisters{})
	case *messages.Event:
		id := msg.ID
		if a.Recovering() {
			scenario := "received replayed event"
			a.buildLog.Printf("%s, internal state changed to\n\tinputs -> '%v', outputs -> '%v', msg: %s\n",
				scenario, a.inputsmap, a.outputsmap, msg)
		} else {
			scenario := "received new message"
			a.buildLog.Printf("%s, internal state changed to\n\tinputs -> '%v', outputs -> '%v', msg: %v\n",
				scenario, a.inputsmap, a.outputsmap, msg)
		}
		switch msg.GetType() {
		case messages.INPUT:
			diff := msg.GetValue() - a.rawInputsmap[id]
			if a.Recovering() {
				a.inputsmap[id] += diff
				a.allInputsmap[id] += diff
				a.rawInputsmap[id] = msg.GetValue()
				break
			}
			if diff > 0 && diff < 10 {
				v, ok := a.puertas[uint(id)]
				//TODO: default state is 1
				if !a.countCloseDoor[int(id)] && (!ok || v != func() uint {
					if v, ok := a.openState[int(id)]; ok {
						return v
					}
					return 1
				}()) {
					a.warnLog.Printf("counting inputs when door (id: %d) is closed, count: %v", id, diff)
				} else {
					a.inputsmap[id] += diff
					a.rawInputsmap[id] = msg.GetValue()
					a.PersistReceive(msg)
					ctx.Send(a.events, &messages.Event{ID: msg.ID, Type: messages.INPUT, Value: diff})
				}
				a.allInputsmap[id] += diff
			} else if diff < 0 {
				a.warnLog.Printf("warning deviation in data (id: %d) -> rawInputs: %d, GetValue() in event: %d", id, a.rawInputsmap, msg.GetValue())
				if msg.GetValue() < 4 {
					ctx.Send(a.events, &messages.Event{ID: msg.ID, Type: messages.INPUT, Value: msg.GetValue()})
					a.inputsmap[id] += msg.GetValue()
					a.allInputsmap[id] += msg.GetValue()
				}
			} else {
				a.warnLog.Printf("counting (id: %d) diff inputs > 10, diff count: %v", id, diff)
			}
			a.rawInputsmap[id] = msg.GetValue()
		case messages.OUTPUT:
			diff := msg.GetValue() - a.rawOutputsmap[id]
			if a.Recovering() {
				a.outputsmap[id] += diff
				a.allOutputsmap[id] += diff
				a.rawOutputsmap[id] = msg.GetValue()
				break
			}
			if diff > 0 && diff < 10 {
				//TODO: back door allways!
				v, ok := a.puertas[uint(id)]
				//TODO: default state is 1
				if !a.countCloseDoor[int(id)] && (!ok || v != func() uint {
					if v, ok := a.openState[int(id)]; ok {
						return v
					}
					return 1
				}()) {
					a.warnLog.Printf("counting outputs when door (id: %d) is closed, count: %v", id, diff)
				} else {
					a.outputsmap[id] += diff
					a.rawOutputsmap[id] = msg.GetValue()
					a.PersistReceive(msg)
					ctx.Send(a.events, &messages.Event{ID: msg.ID, Type: messages.OUTPUT, Value: diff})
				}
				a.allOutputsmap[id] += diff
			} else if diff < 0 {
				a.warnLog.Printf("warning deviation in data (id: %d)-> rawOutputs: %d, GetValue() in event: %d", id, a.rawOutputsmap, msg.GetValue())
				if msg.GetValue() < 4 {
					ctx.Send(a.events, &messages.Event{ID: msg.ID, Type: messages.OUTPUT, Value: msg.GetValue()})
					a.outputsmap[id] += msg.GetValue()
					a.allOutputsmap[id] += msg.GetValue()
				}
			} else {
				a.warnLog.Printf("counting (id: %d) diff outputs > 10, diff count: %v", id, diff)
			}
			a.rawOutputsmap[id] = msg.GetValue()
		case messages.TAMPERING:
			a.tamperingmap[id] += 1
			a.warnLog.Println("tampering")
			ctx.Send(a.events, msg)
		}

	case *msgPingError:
		a.warnLog.Printf("camera keep alive error")
		message := &pubsub.Message{
			Timestamp: float64(time.Now().UnixNano()) / 1000000000,
			Type:      "CounterDisconnected",
			Value:     1,
		}
		data, err := json.Marshal(message)
		if err != nil {
			break
		}
		a.buildLog.Printf("data: %q", data)
		Publish(topicEvents, data)
	case *MsgDoor:
		ctx.Send(a.events, msg)
		a.puertas[msg.ID] = msg.Value
	case *MsgGetGps:
		ctx.RequestWithCustomSender(a.gps, msg, ctx.Sender())
	case *msgEvent:
		a.buildLog.Printf("data: %q", msg)
		Publish(topicEvents, msg.data)
	case *msgAddEvent:
		a.buildLog.Printf("data: %q", msg)
		Publish(topicAddEvents, msg.data)
	case *actor.Terminated:
		a.warnLog.Printf("actor terminated: %s", msg.GetWho().GetAddress())
	}
}
