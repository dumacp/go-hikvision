package client

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client/messages"
	MQTT "github.com/eclipse/paho.mqtt.golang"
)

const (
	clietnName   = "go-nmea-actor"
	topicEvents  = "EVENTS/backcounter"
	topicCounter = "COUNTERBACKDOOR"
)

type msgGPS struct {
	data []byte
}

//ActorPubsub actor to send mesages to MQTTT broket
type ActorPubsub struct {
	*Logger
	clientMqtt MQTT.Client
	debug      bool
}

//NewPubSubActor create PubSubActor
func NewPubSubActor() *ActorPubsub {
	act := &ActorPubsub{}
	act.Logger = &Logger{}
	return act
}

type register struct {
	Registers []uint32 `json:"registers"`
}

//Receive func Receive to actor
func (act *ActorPubsub) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		act.initLogs()
		act.infoLog.Printf("actor started \"%s\"", ctx.Self().Id)
		chGPS := make(chan []byte, 0)
		clientMqtt, err := connectMqtt(chGPS)
		if err != nil {
			panic(err)
		}

		act.clientMqtt = clientMqtt
	case *messages.Snapshot:
		reg := &register{}
		reg.Registers = []uint32{msg.Inputs, msg.Outputs}
		data, err := json.Marshal(reg)
		if err != nil {
			act.errLog.Println(err)
			break
		}
		token := act.clientMqtt.Publish(topicCounter, 0, false, data)
		if ok := token.WaitTimeout(3 * time.Second); !ok {
			act.clientMqtt.Disconnect(100)
			act.errLog.Panic("MQTT connection failed")
		}
	case *msgEvent:
		// fmt.Printf("event: %s\n", msg.event)
		token := act.clientMqtt.Publish(topicEvents, 0, false, msg.data)
		if ok := token.WaitTimeout(3 * time.Second); !ok {
			act.clientMqtt.Disconnect(100)
			act.errLog.Panic("MQTT connection failed")
		}
	}
}

func onMessageWithChannel(ch chan []byte) func(c MQTT.Client, msg MQTT.Message) {
	onMessage := func(c MQTT.Client, msg MQTT.Message) {
		select {
		case ch <- msg.Payload():
		case <-time.After(3 * time.Second):
		}
	}
	return onMessage
}

func connectMqtt(ch chan []byte) (MQTT.Client, error) {
	opts := MQTT.NewClientOptions().AddBroker("tcp://127.0.0.1:1883")
	opts.SetClientID(clietnName)
	opts.SetAutoReconnect(true)
	conn := MQTT.NewClient(opts)
	conn.Subscribe("GPS", 0, onMessageWithChannel(ch))
	token := conn.Connect()
	if ok := token.WaitTimeout(30 * time.Second); !ok {
		return nil, fmt.Errorf("MQTT connection failed")
	}
	return conn, nil
}
