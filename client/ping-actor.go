package client

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

const (
	// pingIP es la dirección ESTÁNDAR de la cámara en el despliegue normal, y el
	// respaldo del keep-alive cuando no se pasa -camera. No está mal elegida: es la
	// que de verdad tienen los equipos en campo, y por eso se conserva intacta.
	pingIP = "192.168.188.21"
)

//PingActor type
type PingActor struct {
	ctx  actor.Context
	quit chan int
	// addr es la cámara a la que se le hace el keep-alive. Vacío usa pingIP.
	//
	// Sin esto el actor consultaba SIEMPRE pingIP, que es la dirección del despliegue
	// estándar pero no la de un equipo con otro direccionamiento —un banco de pruebas,
	// una flota con otro plan de IPs—. Ahí el GET falla cada ciclo y se publica un
	// CounterDisconnected cada 300 s mientras las cámaras funcionan y cuentan. Medido
	// en FLO-W7-0138, con las cámaras en 192.168.186.91 y .93: dos falsas alarmas en
	// cinco minutos. Una alarma que miente es peor que no tenerla, porque entrena a
	// ignorar la que sí importa.
	addr string
	// power lleva el estado de alimentación de la cámara desde el hilo del actor hasta
	// la goroutine del keep-alive. Va por canal y no por un campo porque Receive lo
	// escribiría mientras la goroutine lo lee.
	power chan bool
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
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		log.Printf("actor started \"%s\"", ctx.Self().Id)
		act.quit = make(chan int)
		act.power = make(chan bool, 1)
		act.keepAlive(act.quit)
	case *actor.Stopping:
		select {
		case act.quit <- 1:
		case <-time.After(1 * time.Second):
		}
	case *MsgCameraPower:
		// Con la cámara apagada a propósito el GET fallaría cada ciclo y publicaría un
		// CounterDisconnected cada 300 s: exactamente la falsa alarma que este actor
		// dejó de producir al dejar de consultar una IP fija.
		log.Printf("keep alive de la camara: encendida=%v", msg.On)
		act.avisarPower(msg.On)
	}
}

// avisarPower deja el último estado en el canal, descartando el anterior si la
// goroutine todavía no lo leyó: lo que importa es el estado actual, no la secuencia.
func (act *PingActor) avisarPower(encendida bool) {
	if act.power == nil {
		return
	}
	select {
	case <-act.power:
	default:
	}
	select {
	case act.power <- encendida:
	default:
	}
}

type msgPingError struct{}

func (act *PingActor) keepAlive(quit chan int) {

	power := act.power
	go func() {
		// encendida arranca en true: sin -cameraPowerGPIO nadie manda MsgCameraPower
		// nunca, y el keep-alive se comporta igual que siempre.
		encendida := true
		t1 := time.NewTimer(10 * time.Second)
		defer t1.Stop()
		for {
			select {
			case v := <-power:
				encendida = v
				if encendida {
					// Al volver la energía no se espera el ciclo largo: la cámara
					// recién arrancó y queremos saber enseguida si responde.
					t1.Reset(10 * time.Second)
				}
			case <-t1.C:
				if !encendida {
					t1.Reset(60 * time.Second)
					break
				}
				resp, err := http.Get(fmt.Sprintf("http://%s", act.destino()))
				if err != nil {
					act.ctx.Send(act.ctx.Parent(), &msgPingError{})
					t1.Reset(300 * time.Second)
					break
				}
				resp.Body.Close()

				t1.Reset(60 * time.Second)
			case <-quit:
				return
			}
		}
	}()
}
