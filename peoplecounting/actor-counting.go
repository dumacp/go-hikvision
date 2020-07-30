package peoplecounting

import (
	"log"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/AsynkronIT/protoactor-go/persistence"
	"github.com/dumacp/go-hikvision/peoplecounting/messages"
)

type CountingActor struct {
	persistence.Mixin
	inputs  uint32
	outputs uint32
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

func (a *CountingActor) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		log.Println("actor started")
	case *persistence.RequestSnapshot:
		log.Printf("snapshot internal state: inputs -> '%v', outputs -> '%v'\n",
			a.inputs, a.outputs)
		a.PersistSnapshot(
			&messages.Snapshot{
				Inputs:  a.inputs,
				Outputs: a.outputs,
			})
	case *messages.Snapshot:
		a.inputs = msg.GetInputs()
		a.outputs = msg.GetOutputs()
		log.Printf("recovered from snapshot, internal state changed to:\n\tinputs -> '%v', outputs -> '%v'\n",
			a.inputs, a.outputs)
	case *persistence.ReplayComplete:
		log.Printf("replay completed, internal state changed to:\n\tinputs -> '%v', outputs -> '%v'\n",
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
		log.Printf("%s, internal state changed to\n\tinputs -> '%v', outputs -> '%v'\n",
			scenario, a.inputs, a.outputs)
	}
}
