package client

import (
	"log"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
)

const (
	gpioPuerta1 uint = 162
	gpioPuerta2 uint = 163
)

//DoorsActor type
type DoorsActor struct {
	ctx  actor.Context
	quit chan int
}

//Receive function to actor Receive messages
func (act *DoorsActor) Receive(ctx actor.Context) {
	act.ctx = ctx
	switch ctx.Message().(type) {
	case *actor.Started:
		log.Printf("actor started \"%s\"", ctx.Self().Id)
		act.quit = make(chan int, 0)
		act.listenGpio(act.quit)
	case *msgGpioError:
		panic("error gpio")
	case *actor.Stopping:
		select {
		case act.quit <- 1:
		case <-time.After(1 * time.Second):
		}
	}
}

type msgDoor struct {
	id    uint
	value uint
}

type msgGpioError struct{}

func (act *DoorsActor) listenGpio(quit chan int) {
	chPuertas, err := gpioNewWatcher(quit, gpioPuerta1, gpioPuerta2)
	if err != nil {
		log.Panic(err)
	}
	go func() {
		for v := range chPuertas {
			act.ctx.Send(act.ctx.Parent(), &msgDoor{id: v[0], value: v[1]})
		}

		act.ctx.Send(act.ctx.Self(), &msgGpioError{})
	}()
}
