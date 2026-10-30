package client

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

const (
	// pingIP es la dirección histórica del keep-alive, de cuando el binario atendía un
	// solo despliegue. Se mantiene como respaldo para los equipos que corren SIN
	// -camera, que siguen comportándose exactamente igual que antes.
	pingIP = "192.168.188.21"
)

//PingActor type
type PingActor struct {
	ctx  actor.Context
	quit chan int
	// addr es la cámara a la que se le hace el keep-alive. Vacío usa pingIP.
	//
	// Sin esto el actor consultaba SIEMPRE 192.168.188.21, que en un gateway con otro
	// direccionamiento no existe: el GET falla cada ciclo y se publica un
	// CounterDisconnected cada 300 s mientras las cámaras funcionan y cuentan. Medido
	// en FLO-W7-0138, con las cámaras en 192.168.186.91 y .93: dos falsas alarmas en
	// cinco minutos. Una alarma que miente es peor que no tenerla, porque entrena a
	// ignorar la que sí importa.
	addr string
}

// destino devuelve la dirección a consultar.
func (act *PingActor) destino() string {
	if act.addr != "" {
		return act.addr
	}
	return pingIP
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
				resp, err := http.Get(fmt.Sprintf("http://%s", act.destino()))
				if err != nil {
					act.ctx.Send(act.ctx.Parent(), &msgPingError{})
					t1.Reset(300 * time.Second)
					break
				}
				resp.Body.Close()

				t1.Reset(60 * time.Second)
			case <-act.quit:
				return
			}
		}
	}()
}
