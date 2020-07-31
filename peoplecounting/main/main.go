package main

import (
	"fmt"
	"log"
	"time"

	console "github.com/AsynkronIT/goconsole"
	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/AsynkronIT/protoactor-go/persistence"
	pdb "github.com/dumacp/go-actors/persistence"
	"github.com/dumacp/go-hikvision/peoplecounting"
	"github.com/dumacp/go-hikvision/peoplecounting/messages"
	"github.com/golang/protobuf/proto"
)

type Provider struct {
	providerState persistence.ProviderState
}

var parseEvent = func(src []byte) proto.Message {
	i := new(messages.Event)
	err := proto.Unmarshal(src, i)
	if err != nil {
		log.Println(err)
		return nil
	}
	return i
}

var parseSnapshot = func(src []byte) proto.Message {
	i := new(messages.Snapshot)
	err := proto.Unmarshal(src, i)
	if err != nil {
		log.Println(err)
		return nil
	}
	log.Printf("recovery SNAP: %v", i)
	return i
}

func NewProvider(snapshotInterval int) (*Provider, error) {
	db, err := pdb.NewBoltdbProvider(
		"/tmp/boltdb-counting",
		snapshotInterval,
		parseEvent,
		parseSnapshot,
	)
	if err != nil {
		return nil, err
	}
	return &Provider{
		providerState: db,
	}, nil
}

func (p *Provider) GetState() persistence.ProviderState {
	return p.providerState
}

func main() {
	provider, err := NewProvider(4)
	if err != nil {
		log.Fatalln(err)
	}

	rootContext := actor.EmptyRootContext
	props := actor.PropsFromProducer(func() actor.Actor { return &peoplecounting.CountingActor{} }).WithReceiverMiddleware(persistence.Using(provider))
	pid, _ := rootContext.SpawnNamed(props, "persistent")

	for i := 0; i < 100*1024; i++ {
		rootContext.Send(pid, &messages.Event{Type: messages.INPUT, Value: 1})
		rootContext.Send(pid, &messages.Event{Type: messages.INPUT, Value: 1})
		rootContext.Send(pid, &messages.Event{Type: messages.INPUT, Value: 1})
		rootContext.Send(pid, &messages.Event{Type: messages.INPUT, Value: 1})
		rootContext.Send(pid, &messages.Event{Type: messages.INPUT, Value: 3})
		rootContext.Send(pid, &messages.Event{Type: messages.OUTPUT, Value: 2})
		if i%200 == 0 {
			time.Sleep(300 * time.Millisecond)
		}
	}

	// time.Sleep(3 * time.Second)

	rootContext.PoisonFuture(pid).Wait()
	fmt.Printf("*** restart ***\n")
	pid, _ = rootContext.SpawnNamed(props, "persistent")

	console.ReadLine()
}
