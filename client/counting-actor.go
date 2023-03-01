package client

import (
	"encoding/json"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/AsynkronIT/protoactor-go/persistence"
	"github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/pubsub"
)

// CountingActor struct
type CountingActor struct {
	persistence.Mixin
	*Logger
	// flagRecovering bool
	openState      uint
	countCloseDoor bool
	puertas        map[uint]uint
	inputs         int64
	outputs        int64
	rawInputs      int64
	rawOutputs     int64
	allInputs      int64
	allOutputs     int64

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
	count := &CountingActor{}
	count.Logger = &Logger{}
	count.puertas = make(map[uint]uint)
	return count
}

// SetZeroOpenState set the open state in gpio door
func (a *CountingActor) SetZeroOpenState(state bool) {
	if state {
		a.openState = 0
	} else {
		a.openState = 1
	}
}

// SetZeroOpenState set the open state in gpio door
func (a *CountingActor) SetCountCloseDoor(state bool) {
	if state {
		a.countCloseDoor = true
	} else {
		a.countCloseDoor = false
	}
}

type register struct {
	Registers []int64 `json:"registers"`
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
		events.openState = a.openState
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
			a.inputs, a.outputs, a.rawInputs, a.rawOutputs, a.allInputs, a.allOutputs)
		snap := &messages.Snapshot{
			Inputs:     a.inputs,
			Outputs:    a.outputs,
			RawInputs:  a.rawInputs,
			RawOutputs: a.rawOutputs,
			AllInputs:  a.allInputs,
			AllOutputs: a.allOutputs,
		}
		a.PersistSnapshot(snap)
		ctx.Send(ctx.Self(), &MsgSendRegisters{})

	case *MsgSendRegisters:
		if a.outputs <= 0 && a.inputs <= 0 {
			break
		}
		reg := &register{}
		reg.Registers = []int64{a.outputs, a.inputs}
		data, err := json.Marshal(reg)
		if err != nil {
			a.errLog.Println(err)
			break
		}
		a.buildLog.Printf("data: %q", data)
		Publish(topicCounter, data)
	case *messages.Snapshot:
		a.inputs = msg.GetInputs()
		a.outputs = msg.GetOutputs()
		a.rawInputs = msg.GetRawInputs()
		a.rawOutputs = msg.GetRawOutputs()
		a.allInputs = msg.GetAllInputs()
		a.allOutputs = msg.GetAllOutputs()

		if a.allInputs < a.inputs {
			a.allInputs = a.inputs
		}
		if a.allOutputs < a.outputs {
			a.allOutputs = a.outputs
		}
		a.infoLog.Printf("recovered from snapshot, internal state changed to:\n\tinputs -> '%v', outputs -> '%v', rawInputs -> %v, rawOutpts -> %v, allInputs -> %v, allOutpts -> %v\n",
			a.inputs, a.outputs, a.rawInputs, a.rawOutputs, a.allInputs, a.allOutputs)
		reg := &register{}
		reg.Registers = []int64{msg.Inputs, msg.Outputs}
		data, err := json.Marshal(reg)
		if err != nil {
			a.errLog.Println(err)
			break
		}
		a.buildLog.Printf("data: %q", data)
		Publish(topicCounter, data)
	case *persistence.ReplayComplete:
		a.infoLog.Printf("replay completed, internal state changed to:\n\tinputs -> '%v', outputs -> '%v', rawInputs -> %v, rawOutpts -> %v, allInputs -> %v, allOutpts -> %v\n",
			a.inputs, a.outputs, a.rawInputs, a.rawOutputs, a.allInputs, a.allOutputs)
		// a.PersistSnapshot(snap)
		ctx.Send(ctx.Self(), &MsgSendRegisters{})
	case *messages.Event:
		if a.Recovering() {
			// a.flagRecovering = true
			scenario := "received replayed event"
			a.buildLog.Printf("%s, internal state changed to\n\tinputs -> '%v', outputs -> '%v'\n",
				scenario, a.inputs, a.outputs)
		} else {
			a.PersistReceive(msg)
			scenario := "received new message"
			a.buildLog.Printf("%s, internal state changed to\n\tinputs -> '%v', outputs -> '%v'\n",
				scenario, a.inputs, a.outputs)
		}
		switch msg.GetType() {
		case messages.INPUT:
			diff := msg.GetValue() - a.rawInputs
			if diff > 0 && diff < 10 {

				v, ok := a.puertas[backdoorID]
				if !a.countCloseDoor && (!ok || v != a.openState) {
					a.warnLog.Printf("counting inputs when door is closed, count: %v", diff)
				} else {
					a.inputs += diff
					if !a.Recovering() {
						ctx.Send(a.events, &messages.Event{Type: messages.INPUT, Value: diff})
					}
				}
				a.allInputs += diff
			} else if diff < 0 {
				if !a.Recovering() {
					a.warnLog.Printf("warning deviation in data -> rawInputs: %d, GetValue() in event: %d", a.rawInputs, msg.GetValue())
					if msg.GetValue() < 4 {
						ctx.Send(a.events, &messages.Event{Type: messages.INPUT, Value: msg.GetValue()})
						a.inputs += msg.GetValue()
						a.allInputs += msg.GetValue()
					}
				}
			}
			a.rawInputs = msg.GetValue()
		case messages.OUTPUT:
			diff := msg.GetValue() - a.rawOutputs
			if diff > 0 && diff < 10 {
				//TODO: back door allways!

				v, ok := a.puertas[backdoorID]
				if !a.countCloseDoor && (!ok || v != a.openState) {
					a.warnLog.Printf("counting outputs when door is closed, count: %v", diff)
				} else {
					a.outputs += diff
					if !a.Recovering() {
						ctx.Send(a.events, &messages.Event{Type: messages.OUTPUT, Value: diff})
					}
				}
				a.allOutputs += diff
			} else if diff < 0 {
				if !a.Recovering() {
					a.warnLog.Printf("warning deviation in data -> rawOutputs: %d, GetValue() in event: %d", a.rawOutputs, msg.GetValue())
					if msg.GetValue() < 4 {
						ctx.Send(a.events, &messages.Event{Type: messages.OUTPUT, Value: msg.GetValue()})
						a.outputs += msg.GetValue()
						a.allOutputs += msg.GetValue()
					}
				}
			}
			a.rawOutputs = msg.GetValue()
		case messages.TAMPERING:
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
	case *actor.Terminated:
		a.warnLog.Printf("actor terminated: %s", msg.GetWho().GetAddress())
	}
}
