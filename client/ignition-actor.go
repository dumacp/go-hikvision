package client

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

// topicIgnition es donde go-ignition publica el estado del vehículo. Verificado contra
// el binario que corre en el gateway (/usr/sbin/omvz7/ignition): es la constante
// TopicEvents de su internal/pubsub.
const topicIgnition = "EVENTS/ignition"

const (
	// tipoSuspendEvt es el evento que importa. go-ignition lo emite en el MISMO bloque
	// que su PowerEvent interno, o sea DESPUÉS de su propio periodo de gracia tras la
	// llave (ver la función `down` de su internal/app/lcd-actor.go). Ya viene
	// antirrebote: es "apagá los periféricos ahora", no "la llave se movió".
	tipoSuspendEvt = "SUSPEND_EVT"
	// tipoIgnitionEvt es el estado crudo de la llave, SIN periodo de gracia. Se
	// registra para diagnóstico y no se actúa sobre él: hacerlo apagaría y encendería
	// la cámara con cada golpe de llave, y cada encendido cuesta dos minutos de arranque
	// en los que no se cuenta a nadie.
	tipoIgnitionEvt = "IgnitionEvent"
)

// Estados de go-ignition, del enum StateType de su .proto.
const (
	ignicionNA   = 0
	ignicionUP   = 1
	ignicionDOWN = 2
)

// MsgIgnition es un evento de go-ignition ya parseado.
type MsgIgnition struct {
	// Tipo distingue el aviso con gracia (tipoSuspendEvt) del estado crudo de la llave.
	Tipo      string
	Encendido bool
	// Timestamp es el reloj de go-ignition, no el nuestro.
	Timestamp time.Time
}

// parseIgnitionEvents lee el JSON que publica go-ignition. Capturado del gateway:
//
//	{"value":{"state":1},"timestamp":1793335980.405281,"type":"SUSPEND_EVT"}
//
// Ojo con el `timestamp`: acá son SEGUNDOS con fracción, mientras que el PowerEvent que
// el mismo binario manda por protoactor los lleva en milisegundos enteros. Son campos
// distintos de mensajes distintos aunque se llamen igual.
func parseIgnitionEvents(msg []byte) (interface{}, error) {
	evento := struct {
		Value *struct {
			State int    `json:"state"`
			Coord string `json:"coord"`
		} `json:"value"`
		Timestamp float64 `json:"timestamp"`
		Type      string  `json:"type"`
	}{}

	if err := json.Unmarshal(msg, &evento); err != nil {
		return nil, fmt.Errorf("evento de ignition ilegible: %w", err)
	}
	if evento.Type == "" {
		return nil, fmt.Errorf("evento de ignition sin campo \"type\": %.120s", msg)
	}
	if evento.Value == nil {
		return nil, fmt.Errorf("evento de ignition %q sin campo \"value\"", evento.Type)
	}

	res := &MsgIgnition{Tipo: evento.Type, Timestamp: horaDeIgnition(evento.Timestamp)}
	switch evento.Value.State {
	case ignicionUP:
		res.Encendido = true
	case ignicionDOWN:
		res.Encendido = false
	case ignicionNA:
		// NA es "no sé", y no es lo mismo que apagado. Tratarlo como apagado dejaría la
		// cámara sin energía por un dato que go-ignition mismo declara desconocido.
		return nil, fmt.Errorf("evento de ignition %q con estado NA, se descarta", evento.Type)
	default:
		return nil, fmt.Errorf("evento de ignition %q con estado %d desconocido",
			evento.Type, evento.Value.State)
	}
	return res, nil
}

// horaDeIgnition convierte los segundos con fracción a time.Time. Un timestamp en cero
// —go-ignition lo omite si falla al armarlo— se reemplaza por el reloj local: es
// preferible una hora aproximada a una de 1970.
func horaDeIgnition(segundos float64) time.Time {
	if segundos <= 0 {
		return time.Now()
	}
	entero, fraccion := math.Modf(segundos)
	return time.Unix(int64(entero), int64(fraccion*float64(time.Second)))
}

// IgnitionActor escucha el estado del vehículo y se lo pasa al padre.
//
// Es deliberadamente delgado: no toca la GPIO ni decide secuencias. Solo traduce el
// tópico de MQTT a un mensaje interno y filtra el ruido, para que la política de
// apagado viva en un solo lugar (CountingActor).
type IgnitionActor struct {
	*Logger
}

// NewIgnitionActor create IgnitionActor
func NewIgnitionActor() *IgnitionActor {
	a := &IgnitionActor{}
	a.Logger = &Logger{}
	return a
}

// Receive func Receive in actor
func (a *IgnitionActor) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		a.initLogs()
		a.infoLog.Printf("actor started %q, escuchando %q", ctx.Self().Id, topicIgnition)
		if err := SubscribeFun(topicIgnition, ctx.Self(), parseIgnitionEvents); err != nil {
			time.Sleep(3 * time.Second)
			a.errLog.Panicln(err)
		}

	case *actor.Stopping:
		a.warnLog.Println("stopped actor")

	case *MsgIgnition:
		if msg.Tipo != tipoSuspendEvt {
			// El estado crudo de la llave solo se registra. Ver tipoIgnitionEvt.
			a.buildLog.Printf("ignition %s: encendido=%v (%s), sin acción",
				msg.Tipo, msg.Encendido, msg.Timestamp.Format(time.RFC3339))
			break
		}
		a.infoLog.Printf("ignition %s: vehiculo encendido=%v (%s)",
			msg.Tipo, msg.Encendido, msg.Timestamp.Format(time.RFC3339))
		if ctx.Parent() != nil {
			ctx.Send(ctx.Parent(), msg)
		}
	}
}
