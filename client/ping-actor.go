package client

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
)

const (
	pingIP = "192.168.188.21"
)

//PingActor type
type PingActor struct {
	ctx  actor.Context
	quit chan int
}

//Receive function to actor Receive messages
func (act *PingActor) Receive(ctx actor.Context) {
	act.ctx = ctx
	switch ctx.Message().(type) {
	case *actor.Started:
		log.Printf("actor started \"%s\"", ctx.Self().Id)
		act.quit = make(chan int, 0)
		act.keepAlive(act.quit)
	case *actor.Stopping:
		select {
		case act.quit <- 1:
		case <-time.After(1 * time.Second):
		}
	}
}

type msgPingError struct{}

func (act *PingActor) keepAlive(quit chan int) {

	go func() {
		t1 := time.NewTimer(10 * time.Second)
		defer t1.Stop()
		for {
			select {
			case <-t1.C:
				resp, err := http.Get(fmt.Sprintf("http://%s", pingIP))
				resp.Body.Close()
				if err != nil {
					act.ctx.Send(act.ctx.Parent(), &msgPingError{})
					t1.Reset(300 * time.Second)
					break
				}

				t1.Reset(60 * time.Second)
			case <-act.quit:
				return
			}
		}
	}()
}
