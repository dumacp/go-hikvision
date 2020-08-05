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
	inputs  uint32
	outputs uint32

	pubsub *actor.PID
	doors  *actor.PID
	events *actor.PID
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
		pid1, err := actor.SpawnNamed(props1, "pubsub")
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
		events.initLogs()
		props2 := actor.PropsFromFunc(events.Receive)
		pid2, err := actor.SpawnNamed(props2, "events")
		if err != nil {
			a.errLog.Panicln(err)
		}
		a.events = pid2

		props3 := actor.PropsFromProducer(func() actor.Actor { return &DoorsActor{} })
		pid3, err := actor.SpawnNamed(props3, "doors")
		if err != nil {
			a.errLog.Panicln(err)
		}
		a.doors = pid3

	case *persistence.RequestSnapshot:
		a.buildLog.Printf("snapshot internal state: inputs -> '%v', outputs -> '%v'\n",
			a.inputs, a.outputs)
		snap := &messages.Snapshot{
			Inputs:  a.inputs,
			Outputs: a.outputs,
		}
		a.PersistSnapshot(snap)
		ctx.Send(a.pubsub, snap)
	case *messages.Snapshot:
		a.inputs = msg.GetInputs()
		a.outputs = msg.GetOutputs()
		a.infoLog.Printf("recovered from snapshot, internal state changed to:\n\tinputs -> '%v', outputs -> '%v'\n",
			a.inputs, a.outputs)
		ctx.Send(a.pubsub, msg)
	case *persistence.ReplayComplete:
		a.infoLog.Printf("replay completed, internal state changed to:\n\tinputs -> '%v', outputs -> '%v'\n",
			a.inputs, a.outputs)
	case *messages.Event:
		scenario := "received replayed event"
		if !a.Recovering() {
			a.PersistReceive(msg)
			scenario = "received new message"
		}
		if msg.GetType() == messages.INPUT {
			a.inputs += msg.GetValue()
		} else {
			a.outputs += msg.GetValue()
		}
		a.buildLog.Printf("%s, internal state changed to\n\tinputs -> '%v', outputs -> '%v'\n",
			scenario, a.inputs, a.outputs)
	case *msgDoor:
		ctx.Send(a.events, msg)
	case *msgGPS:
		ctx.Send(a.events, msg)
	case *msgEvent:
		ctx.Send(a.pubsub, msg)
	case *actor.Terminated:
		a.warnLog.Printf("actor terminated: %s", msg.GetWho().GetAddress())
	}
}
