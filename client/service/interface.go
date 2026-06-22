package service

import (
	"github.com/asynkron/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client/service/messages"
)

//Service interface
type Service interface {
	//Start
	Start()
	Stop()
	Restart()
	Status() *messages.StatusResponse

	Info(ctx actor.Context, pid *actor.PID) (*messages.InfoCounterResponse, error)
}
