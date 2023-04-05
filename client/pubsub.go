package client

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/dumacp/go-logs/pkg/logs"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	clientID       = "camera"
	TopicAppliance = "appliance/camera"
	topicEvents    = "EVENTS/backcounter"
	//topicCounter          = "COUNTERBACKDOOR"
	topicCounter          = "COUNTERSMAPDOOR"
	TopicStart            = TopicAppliance + "/START"
	TopicRestart          = TopicAppliance + "/RESTART"
	TopicStop             = TopicAppliance + "/STOP"
	TopicStatus           = TopicAppliance + "/STATUS"
	TopicRequestInfoState = TopicAppliance + "/RequestInfoState"
)

// //Gateway interface
// type Gateway interface {
// 	Receive(ctx actor.Context)
// 	// Publish(topic string, msg []byte)
// }

type pubsubActor struct {
	ctx actor.Context
	// rootctx *actor.RootContext
	// behavior      actor.Behavior
	// state         messages.StatusResponse_StateType
	client mqtt.Client
	// mux           sync.Mutex
	subscriptions map[string]*subscribeMSG
}

var instance *pubsubActor
var once sync.Once

// getInstance create pubsub Gateway
func getInstance(ctx *actor.RootContext) *pubsubActor {

	once.Do(func() {
		instance = &pubsubActor{}
		// instance.mux = sync.Mutex{}
		instance.subscriptions = make(map[string]*subscribeMSG)
		if ctx == nil {
			ctx = actor.NewActorSystem().Root
		}
		props := actor.PropsFromFunc(instance.Receive)
		_, err := ctx.SpawnNamed(props, "pubsub-actor")
		if err != nil {
			logs.LogError.Panic(err)
		}
		time.Sleep(100 * time.Millisecond)
	})

	return instance
}

// Init init pubsub instance
func InitPubSub(ctx *actor.RootContext) error {
	defer time.Sleep(3 * time.Second)
	if getInstance(ctx) == nil {
		return fmt.Errorf("error instance")
	}
	return nil
}

type publishMSG struct {
	topic string
	msg   []byte
}

type subscribeMSG struct {
	pid   *actor.PID
	parse func([]byte) (interface{}, error)
}

// Publish function to publish messages in pubsub gateway
func Publish(topic string, msg []byte) {
	getInstance(nil).ctx.Send(instance.ctx.Self(), &publishMSG{topic: topic, msg: msg})
}

// Subscribe subscribe to topics
func SubscribeFun(topic string, pid *actor.PID, parse func([]byte) (interface{}, error)) error {
	instance := getInstance(nil)
	subs := &subscribeMSG{pid: pid, parse: parse}
	// instance.mux.Lock()
	instance.subscriptions[topic] = subs
	// instance.mux.Unlock()
	if !instance.client.IsConnected() {
		// instance.ctx.PoisonFuture(instance.ctx.Self()).Wait()
		return fmt.Errorf("pubsub is not connected")
	}
	logs.LogBuild.Printf("subscription in topic -> %q -> %#v", topic, subs)
	instance.subscribe(topic, subs)
	return nil
}

func (ps *pubsubActor) subscribe(topic string, subs *subscribeMSG) error {
	handler := func(client mqtt.Client, m mqtt.Message) {
		// logs.LogBuild.Printf("local topic -> %q", m.Topic())
		// logs.LogBuild.Printf("local payload - > %s", m.Payload())
		m.Ack()
		msg, err := subs.parse(m.Payload())
		if err != nil {
			log.Println(err)
			return
		}
		// logs.LogBuild.Printf("parse payload-> %s", msg)
		ps.ctx.Send(subs.pid, msg)
	}
	if tk := instance.client.Subscribe(topic, 1, handler); !tk.WaitTimeout(3 * time.Second) {
		if err := tk.Error(); err != nil {
			return err
		}
	}
	return nil
}

// Receive function
func (ps *pubsubActor) Receive(ctx actor.Context) {
	logs.LogBuild.Printf("Message arrived in pubsubActor: %s, %T, %s",
		ctx.Message(), ctx.Message(), ctx.Sender())
	ps.ctx = ctx
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		logs.LogInfo.Printf("Starting, actor, pid: %v\n", ctx.Self())
		ps.client = client()
		if err := connect(ps.client); err != nil {
			time.Sleep(3 * time.Second)
			logs.LogError.Panic(err)
		}
		for k, v := range ps.subscriptions {
			ps.subscribe(k, v)
		}
	case *publishMSG:
		// fmt.Printf("publish msg: %s", msg.msg)
		fmt.Printf("publish msg: %s\n", msg.msg)
		tk := ps.client.Publish(msg.topic, 0, false, msg.msg)
		if !tk.WaitTimeout(3 * time.Second) {
			if tk.Error() != nil {
				logs.LogError.Printf("end error: %s, with messages -> %v", tk.Error(), msg)
			} else {
				logs.LogError.Printf("timeout error with message -> %v", msg)
			}
		}
	case *actor.Stopping:
		ps.client.Disconnect(600)
		logs.LogError.Println("Stopping, actor is about to shut down")
	case *actor.Stopped:
		logs.LogError.Println("Stopped, actor and its children are stopped")
	case *actor.Restarting:
		logs.LogError.Println("Restarting, actor is about to restart")
	}
}

// func (ps *pubsubActor) Started(ctx actor.Context) {
// 	ps.ctx = ctx
// 	switch ctx.Message().(type) {
// 	}
// }

// func (ps *pubsubActor) Stopped(ctx actor.Context) {
// }

func client() mqtt.Client {
	opt := mqtt.NewClientOptions().AddBroker("tcp://127.0.0.1:1883")
	opt.SetAutoReconnect(true)
	randBytes := make([]byte, 4)
	rand.Read(randBytes)
	opt.SetClientID(fmt.Sprintf("%s-%s-%d", clientID, hex.EncodeToString(randBytes), time.Now().Unix()))
	opt.SetKeepAlive(30 * time.Second)
	opt.SetConnectRetryInterval(10 * time.Second)
	client := mqtt.NewClient(opt)
	return client
}

func connect(c mqtt.Client) error {
	tk := c.Connect()
	if !tk.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("connect wait error")
	}
	if err := tk.Error(); err != nil {
		return err
	}
	return nil
}
