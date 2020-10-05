package pubsub

import (
	"fmt"
	"log"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client/service"
	"github.com/dumacp/go-hikvision/client/service/messages"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	clientID       = "camera"
	topicAppliance = "appliance/camera/"
)

//Gateway interface
type Gateway interface {
	Receive(ctx actor.Context)
	Publish(topic string, msg []byte)
}

type pubsubActor struct {
	svc         service.Service
	log         *log.Logger
	ctx         actor.Context
	countingPID *actor.PID
	behavior    actor.Behavior
	state       messages.StatusResponse_StateType
	client      mqtt.Client
}

//New create pubsub Gateway
func New(svc service.Service) Gateway {
	return &pubsubActor{svc: svc}
}

func (svc *pubsubActor) Publish(topic string, msg []byte) {

}

//Receive function
func (svc *pubsubActor) Receive(ctx actor.Context) {
	switch ctx.Message().(type) {
	case *actor.Started:
		svc.log.Printf("Starting, actor, pid: %v\n", ctx.Self())
		svc.client = client()
		if err := connect(svc.client); err != nil {
			time.Sleep(3 * time.Second)
			svc.log.Panic(err)
		}
	case *actor.Stopping:
		svc.client.Disconnect(600)
		svc.log.Println("Stopping, actor is about to shut down")
	case *actor.Stopped:
		svc.log.Println("Stopped, actor and its children are stopped")
	case *actor.Restarting:
		svc.log.Println("Restarting, actor is about to restart")
	default:
		svc.behavior.Receive(ctx)
	}
}

func (ps *pubsubActor) Started(ctx actor.Context) {
	ps.ctx = ctx
	switch msg := ctx.Message().(type) {
	case *messages.Stop:
		ps.svc.Stop()
	case *messages.Restart:
		ps.svc.Restart()
	case *messages.StatusRequest:
		status := ps.svc.Status()
		topic := fmt.Sprintf("%s/%s", topicAppliance, msg.GetSender())
		payload, err := status.Marshal()
		if err != nil {
			ps.log.Println(err)
			break
		}
		ps.client.Publish(topic, 1, false, payload)
	case *messages.InfoCounterRequest:
		info, err := ps.svc.Info(ctx, ps.countingPID)
		if err != nil {
			ps.log.Println(err)
			break
		}
		topic := fmt.Sprintf("%s/%s", topicAppliance, msg.GetSender())
		payload, err := info.Marshal()
		if err != nil {
			ps.log.Println(err)
			break
		}
		ps.client.Publish(topic, 1, false, payload)
	}
}

func (svc *pubsubActor) Stopped(ctx actor.Context) {
}

func client() mqtt.Client {
	opt := mqtt.NewClientOptions()
	opt.SetAutoReconnect(true)
	opt.SetClientID(fmt.Sprintf("%s-%d", clientID, time.Now().Unix()))
	opt.SetKeepAlive(30 * time.Second)
	opt.SetConnectRetryInterval(10 * time.Second)
	client := mqtt.NewClient(opt)
	return client
}

func connect(c mqtt.Client) error {
	tk := c.Connect()
	if tk.WaitTimeout(3 * time.Second) {
		return fmt.Errorf("connect wait error")
	}
	if err := tk.Error(); err != nil {
		return err
	}
	return nil
}
