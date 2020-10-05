package service

import (
	"fmt"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/AsynkronIT/protoactor-go/eventstream"
	localmsg "github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/go-hikvision/client/service/messages"
)

type service struct {
	state messages.StatusResponse_StateType
}

func (svc *service) Start() {
	svc.state = messages.STARTED
	eventstream.Publish(&localmsg.Start{})
}

func (svc *service) Stop() {
	svc.state = messages.STOPPED
	eventstream.Publish(&localmsg.Stop{})
}

func (svc *service) Restart() {
	svc.state = messages.STOPPED
	eventstream.Publish(&localmsg.Stop{})
	time.Sleep(1 * time.Second)
	eventstream.Publish(&localmsg.Start{})
	svc.state = messages.STARTED
}

func (svc *service) Status() *messages.StatusResponse {
	return &messages.StatusResponse{
		State: svc.state,
	}
}

func (svc *service) Info(ctx actor.Context, pid *actor.PID) (*messages.InfoCounterResponse, error) {
	future := ctx.RequestFuture(pid, &localmsg.InfoCounterRequest{}, time.Second*3)
	err := future.Wait()
	if err != nil {
		return nil, err
	}
	res, err := future.Result()
	if err != nil {
		return nil, err
	}
	msg, ok := res.(*localmsg.InfoCounterResponse)
	if !ok {
		return nil, fmt.Errorf("message error: %T", msg)
	}
	return &messages.InfoCounterResponse{Inputs: msg.Inputs, Outputs: msg.Outputs}, nil
}
