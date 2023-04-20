package client

import (
	"errors"
	"strings"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/dumacp/go-logs/pkg/logs"
	"github.com/dumacp/gpsnmea"
)

const (
	timeout = 30 * time.Second
)

// Actor actor to listen events
type GPSActor struct {
	ctx actor.Context
	// subcriptors  map[string]*actor.PID
	lastRawCoord *MsgGpsRaw
}

func NewGPSActor() actor.Actor {
	return &GPSActor{}
}

func parseGPSEvents(msg []byte) (interface{}, error) {

	event := new(MsgGpsRaw)
	event.Data = msg
	event.Time = time.Now()

	return event, nil
}

// Receive func Receive in actor
func (a *GPSActor) Receive(ctx actor.Context) {
	// logs.LogBuild.Printf("Message arrived in gpsActor: %T, %s",
	// 	ctx.Message(), ctx.Sender())
	// logs.LogBuild.Printf("Message arrived in gpsActor: %s, %T, %s",
	// 	ctx.Message(), ctx.Message(), ctx.Sender())

	a.ctx = ctx
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		logs.LogInfo.Printf("started \"%s\", %v", ctx.Self().GetId(), ctx.Self())
		if err := SubscribeFun("GPS", ctx.Self(), parseGPSEvents); err != nil {
			time.Sleep(3 * time.Second)
			logs.LogError.Panic(err)
		}
	case *actor.Stopping:
		logs.LogWarn.Printf("\"%s\" - Stopped actor, reason -> %v", ctx.Self(), msg)
	case *actor.Restarting:
		logs.LogWarn.Printf("\"%s\" - Restarting actor, reason -> %v", ctx.Self(), msg)
	case *actor.Terminated:
		logs.LogWarn.Printf("\"%s\" - Terminated actor, reason -> %v", ctx.Self(), msg)
	case *MsgGpsRaw:
		switch {
		case strings.HasPrefix(string(msg.Data), "$GPRMC"):
			a.lastRawCoord = msg
		}
	case *MsgGetGps:
		if err := func() error {

			if a.lastRawCoord == nil {
				return errors.New("coord is empty")
			}
			v := gpsnmea.ParseRMC(string(a.lastRawCoord.Data))
			if v == nil {
				return errors.New("coord parse error")
			}
			if a.lastRawCoord.Time.Before(time.Now().Add(-timeout)) {
				return errors.New("last coord is very old")
			}
			if ctx.Sender() != nil {
				ctx.Respond(&MsgGPS{Data: a.lastRawCoord.Data})
			}
			return nil
		}(); err != nil {
			logs.LogWarn.Printf("cord error: %s", err)
			if ctx.Sender() != nil {
				ctx.Respond(&MsgGPS{Data: nil})
			}
		}
	case *MsgRequestStatus:
		if ctx.Sender() != nil {
			break
		}
		ctx.Respond(&MsgStatus{State: true})
	}
}

// MsgGPS GPRMC Data
type MsgGPS struct {
	Data []byte
}

type MsgGpsRaw struct {
	Data []byte
	Time time.Time
}

type MsgGetGps struct{}

type MsgSubscribe struct{}
type MsgRequestStatus struct {
}
type MsgStatus struct {
	State bool
}
