package client

import (
	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/AsynkronIT/protoactor-go/persistence"
	"github.com/dumacp/go-hikvision/client/messages"
)

//CountingActor struct
type CountingActor struct {
	persistence.Mixin
	*Logger
	flagRecovering bool
	inputs         int64
	outputs        int64
	rawInputs      int64
	rawOutputs     int64

	pubsub *actor.PID
	doors  *actor.PID
	events *actor.PID
	ping   *actor.PID
}

//NewCountingActor create CountingActor
func NewCountingActor() *CountingActor {
	count := &CountingActor{}
	count.Logger = &Logger{}
	return count
}

// type Snapshot struct {
// 	Inputs  uint32
// 	Outputs uint32
// }

// func (snap *Snapshot) Reset() { *snap = Snapshot{} }
// func (snap *Snapshot) String() string {
// 	return fmt.Sprintf("{Inputs: %d, Outputs: %d}", snap.Inputs, snap.Outputs)
// }
// func (snap *Snapshot) ProtoMessage() {}

//MsgSendRegisters messages to send registers to pubsub
type MsgSendRegisters struct{}

//Receive function to receive message in actor
func (a *CountingActor) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		a.initLogs()
		a.infoLog.Printf("actor started \"%s\"", ctx.Self().Id)

		pubsub := NewPubSubActor()
		pubsub.SetLogError(a.errLog).
			SetLogWarn(a.warnLog).
			SetLogInfo(a.infoLog).
			SetLogBuild(a.buildLog)
		if a.debug {
			pubsub.WithDebug()
		}
		props1 := actor.PropsFromFunc(pubsub.Receive)
		pid1, err := ctx.SpawnNamed(props1, "pubsub")
		if err != nil {
			a.errLog.Panicln(err)
		}
		a.pubsub = pid1

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

		props3 := actor.PropsFromProducer(func() actor.Actor { return &DoorsActor{} })
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

	case *persistence.RequestSnapshot:
		a.buildLog.Printf("snapshot internal state: inputs -> '%v', outputs -> '%v', rawInputs -> %v, rawOutpts -> %v\n",
			a.inputs, a.outputs, a.rawInputs, a.rawOutputs)
		snap := &messages.Snapshot{
			Inputs:     a.inputs,
			Outputs:    a.outputs,
			RawInputs:  a.rawInputs,
			RawOutputs: a.rawOutputs,
		}
		a.PersistSnapshot(snap)
		ctx.Send(a.pubsub, snap)

	case *MsgSendRegisters:
		if a.outputs <= 0 && a.inputs <= 0 {
			break
		}
		snap := &messages.Snapshot{
			Inputs:     a.inputs,
			Outputs:    a.outputs,
			RawInputs:  a.rawInputs,
			RawOutputs: a.rawOutputs,
		}
		ctx.Send(a.pubsub, snap)

	case *messages.Snapshot:
		a.inputs = msg.GetInputs()
		a.outputs = msg.GetOutputs()
		a.rawInputs = msg.GetRawInputs()
		a.rawOutputs = msg.GetRawOutputs()
		a.infoLog.Printf("recovered from snapshot, internal state changed to:\n\tinputs -> '%v', outputs -> '%v', rawInputs -> %v, rawOutpts -> %v\n",
			a.inputs, a.outputs, a.rawInputs, a.rawOutputs)
		// ctx.Send(a.pubsub, msg)
	case *persistence.ReplayComplete:
		a.infoLog.Printf("replay completed, internal state changed to:\n\tinputs -> '%v', outputs -> '%v'\n",
			a.inputs, a.outputs)
		snap := &messages.Snapshot{
			Inputs:  a.inputs,
			Outputs: a.outputs,
		}
		// a.PersistSnapshot(snap)
		ctx.Send(a.pubsub, snap)
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
		// a.buildLog.Printf("event -> %#v", msg)
		// a.buildLog.Printf("data ->'%v', rawinputs -> '%v', rawoutputs -> '%v' \n",
		// 	msg.GetValue(), a.rawInputs, a.rawOutputs)
		switch msg.GetType() {
		case messages.INPUT:
			diff := msg.GetValue() - a.rawInputs
			if diff > 0 {
				a.inputs += diff
				if !a.Recovering() {
					ctx.Send(a.events, &messages.Event{Type: messages.INPUT, Value: diff})
				}
			} else if diff < 0 {
				a.inputs += msg.GetValue()
				if !a.Recovering() {
					ctx.Send(a.events, msg)
				}
			}
			a.rawInputs = msg.GetValue()
		case messages.OUTPUT:
			diff := msg.GetValue() - a.rawOutputs
			if diff > 0 {
				a.outputs += diff
				if !a.Recovering() {
					ctx.Send(a.events, &messages.Event{Type: messages.OUTPUT, Value: diff})
				}
			} else if diff < 0 {
				a.outputs += msg.GetValue()
				if !a.Recovering() {
					ctx.Send(a.events, msg)
				}
			}
			a.rawOutputs = msg.GetValue()
		case messages.TAMPERING:
			a.warnLog.Println("shelteralarm")
			ctx.Send(a.events, msg)
		}

		// if a.flagRecovering {
		// 	a.flagRecovering = false
		// 	snap := &messages.Snapshot{
		// 		Inputs:     a.inputs,
		// 		Outputs:    a.outputs,
		// 		RawInputs:  a.rawInputs,
		// 		RawOutputs: a.rawOutputs,
		// 	}
		// 	a.PersistSnapshot(snap)
		// }

	case *msgPingError:
		a.warnLog.Printf("camera keep alive error")
		ctx.Send(a.pubsub, msg)
	case *msgDoor:
		ctx.Send(a.events, msg)
	case *msgGPS:
		// a.buildLog.Printf("\"%s\" - msg: '%q'\n", ctx.Self().GetId(), msg)
		ctx.Send(a.events, msg)
	case *msgEvent:
		ctx.Send(a.pubsub, msg)
	case *actor.Terminated:
		a.warnLog.Printf("actor terminated: %s", msg.GetWho().GetAddress())
	}
}
