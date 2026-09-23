package client

import (
	"encoding/json"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/pubsub"
)

const (
	gprmctype = 0
	gngnsType = 1
)

// EventActor type
type EventActor struct {
	*Logger
	puertas map[uint]uint
	// withoutEventID omite el campo event_id del COUNTERSDOOR.
	//
	// Es una compuerta de compatibilidad, no una opción de producto: existe solo
	// mientras la plataforma no sepa procesar ese campo. Se quita el día que la
	// plataforma lo acepte, y entonces el binario corre sin el flag.
	withoutEventID bool
}

// NewEventActor create EventActor
func NewEventActor() *EventActor {
	event := &EventActor{}
	event.Logger = &Logger{}
	event.puertas = make(map[uint]uint)
	return event
}

// SetWithoutEventID omite event_id en el COUNTERSDOOR. Ver el campo homónimo.
func (act *EventActor) SetWithoutEventID(v bool) { act.withoutEventID = v }

type msgEvent struct {
	data []byte
}
type msgAddEvent struct {
	data []byte
}

// Receive function to Receive actor messages
func (act *EventActor) Receive(ctx actor.Context) {

	switch msg := ctx.Message().(type) {
	case *actor.Started:
		act.initLogs()
		act.infoLog.Printf("actor started \"%s\"", ctx.Self().Id)
	case *messages.Event:
		act.buildLog.Printf("\"%s\" - event -> '%v'\n", ctx.Self().GetId(), msg)
		frame := ""
		res, err := ctx.RequestFuture(ctx.Parent(), &MsgGetGps{}, 180*time.Millisecond).Result()
		if err == nil {
			if datagps, ok := res.(*MsgGPS); ok {
				frame = string(datagps.Data)
			}
		}
		var event []byte
		switch msg.Type {
		case messages.Event_INPUT:

			event = buildEventPass(ctx, msg, frame, act.puertas, act.Logger, act.withoutEventID)
			ctx.Send(ctx.Parent(), &msgEvent{data: event})
		case messages.Event_OUTPUT:
			event = buildEventPass(ctx, msg, frame, act.puertas, act.Logger, act.withoutEventID)
			ctx.Send(ctx.Parent(), &msgEvent{data: event})
		case messages.Event_TAMPERING:
			event = buildEventTampering(ctx, msg, frame, act.puertas, act.Logger)
			ctx.Send(ctx.Parent(), &msgAddEvent{data: event})
		}

	case *MsgDoor:
		act.puertas[msg.ID] = msg.Value
		act.buildLog.Printf("arrived door to events-actor: %v\n", msg)
	case *actor.Stopped:
		act.infoLog.Println("stoped actor")
	}
}

func buildEventPass(ctx actor.Context, v *messages.Event, gps string, puerta map[uint]uint,
	log *Logger, withoutEventID bool) []byte {
	// tn := time.Now()

	// log.buildLog.Printf("memorys, %v, %v", mem1, mem2)
	contadores := []int64{0, 0}
	if v.Type == messages.Event_INPUT {
		contadores[0] = v.Value
	} else if v.Type == messages.Event_OUTPUT {
		contadores[1] = v.Value
	}
	frame := gps

	id := v.ID
	doorState := uint(0)
	if vm, ok := puerta[uint(id)]; ok {
		doorState = vm
	}

	message := &pubsub.Message{
		Timestamp: float64(time.Now().UnixNano()) / 1000000000,
		Type:      "COUNTERSDOOR",
	}

	uid := v.GetUid()
	if withoutEventID {
		uid = ""
	}

	val := struct {
		Coord    string  `json:"coord"`
		ID       int     `json:"id"`
		State    uint    `json:"state"`
		Counters []int64 `json:"counters"`
		Type     string  `json:"type,omitempty"`
		// EventID es la llave con la que la plataforma une este paso con el evento
		// COUNTERSDOORVIDEO que llega después, cuando el clip ya está en disco. Se
		// omite si el paso no lo trae, como los replicados de una boltdb anterior,
		// y también con -withoutEventID mientras la plataforma no sepa procesarlo.
		//
		// El omitempty es lo que hace innecesario un segundo struct: basta con no
		// rellenar el campo y el JSON sale exactamente como antes de 1.0.32, byte
		// por byte. Un struct alterno abriría la puerta a que las dos formas se
		// separen con el tiempo sin que nadie lo note.
		EventID string `json:"event_id,omitempty"`
	}{
		frame,
		int(v.ID),
		doorState,
		contadores[0:2],
		"CAMERA",
		uid,
	}
	message.Value = val

	msg, err := json.Marshal(message)
	if err != nil {
		log.errLog.Println(err)
	}
	log.buildLog.Printf("%s\n", msg)

	return msg
}

func buildEventTampering(ctx actor.Context, v *messages.Event, gps string, puerta map[uint]uint, log *Logger) []byte {
	// tn := time.Now()

	if v.Type != messages.Event_TAMPERING {
		return nil
	}
	frame := gps

	id := v.ID
	doorState := uint(0)
	if vm, ok := puerta[uint(id)]; ok {
		doorState = vm
	}

	message := &pubsub.Message{
		Timestamp: float64(time.Now().UnixNano()) / 1000000000,
		Type:      "TAMPERING",
	}

	val := struct {
		Coord    string  `json:"coord"`
		ID       int     `json:"id"`
		State    uint    `json:"state"`
		Counters []int64 `json:"counters"`
		Type     string  `json:"type,omitempty"`
	}{
		frame,
		int(id),
		doorState,
		[]int64{0, 0},
		"CAMERA",
	}

	if id == 0 {
		val.Counters[0] = 1
	} else if id == 1 {
		val.Counters[1] = 1
	}
	message.Value = val

	msg, err := json.Marshal(message)
	if err != nil {
		log.errLog.Println(err)
	}
	log.buildLog.Printf("%s\n", msg)

	return msg
}
