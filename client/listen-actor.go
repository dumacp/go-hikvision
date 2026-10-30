package client

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/go-hikvision/peoplecounting"
)

// legacyBackDoorIP is the camera address that used to be hardcoded to identify the
// back door. It is still the fallback when no -camera list is configured, so existing
// deployments keep counting exactly as before.
const legacyBackDoorIP = "192.168.188.21"

// LegacyBackDoorIP exposes the fallback address for the startup banner.
func LegacyBackDoorIP() string { return legacyBackDoorIP }

// resyncThreshold tells a clock resynchronization from an out of order delivery. A
// camera whose clock is corrected backwards (NTP, or a reboot with a default date)
// must not have its events discarded until the clock catches up again.
const resyncThreshold = 60 * time.Second

// ListenActor actor to listen events
type ListenActor struct {
	*Logger
	context       actor.Context
	countingActor *actor.PID
	entersBefore  map[int]int64
	exitsBefore   map[int]int64
	timeBefore    map[int]time.Time
	// ventanaAvisada evita repetir en cada ventana el aviso de que esa camara reporta
	// por intervalo y no por cruce. Es una propiedad del modelo, no un incidente: dicho
	// una vez por puerta alcanza.
	ventanaAvisada map[int]bool

	cancel func()

	socket  string
	cameras []string
}

// SetCameras configures the camera address of each door, where the index is the door
// id. An empty list keeps the historical hardcoded behaviour.
func (act *ListenActor) SetCameras(ips []string) *ListenActor {
	act.cameras = ips
	return act
}

// doorID maps the source address of an event to a door id.
func (act *ListenActor) doorID(remoteIP string) int {
	for id, ip := range act.cameras {
		if len(ip) > 0 && strings.Contains(remoteIP, ip) {
			return id
		}
	}
	if len(act.cameras) > 0 {
		// Counting it as door 0 keeps the passenger in the totals, which beats
		// dropping the event, but the door breakdown is wrong: say so loudly.
		act.warnLog.Printf("event from %q does not match any -camera %v, counted as door 0",
			remoteIP, act.cameras)
		return 0
	}
	if len(remoteIP) == 0 {
		return 1
	}
	if strings.Contains(remoteIP, legacyBackDoorIP) {
		return 1
	}
	return 0
}

// NewListen create listen actor
func NewListen(socket string, countingActor *actor.PID) *ListenActor {
	act := &ListenActor{}
	act.countingActor = countingActor
	act.socket = socket
	act.Logger = &Logger{}
	act.timeBefore = make(map[int]time.Time)
	act.exitsBefore = make(map[int]int64)
	act.entersBefore = make(map[int]int64)
	act.ventanaAvisada = make(map[int]bool)
	return act
}

// Receive func Receive in actor
func (act *ListenActor) Receive(ctx actor.Context) {
	act.context = ctx
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		act.initLogs()
		act.infoLog.Printf("actor started \"%s\"", ctx.Self().Id)
		contxt, cancel := context.WithCancel(context.TODO())
		act.cancel = cancel
		go act.runListen(contxt)
	case *actor.Stopping:
		act.warnLog.Println("stopped actor")
		if act.cancel != nil {
			act.cancel()
		}
	case *messages.CountingActor:
		act.countingActor = actor.NewPID(msg.Address, msg.ID)
	case *msgListenError:
		act.errLog.Panicln("listen error")
	}
}

type msgListenError struct{}

func parseDateTime(t1 string) (time.Time, error) {
	b1 := []byte(t1)
	re := regexp.MustCompile("([0-9]{4}-[0-9]{1,2}-[0-9]{1,2}T[0-9]{1,2}:[0-9]{1,2}:[0-9]{1,2})-([0-9]:00)")
	b2 := re.ReplaceAll(b1, []byte("${1}-0${2}"))
	p1, err := time.Parse(time.RFC3339, string(b2))
	if err != nil {
		return time.Time{}, err
	}
	return p1, nil
}

func (act *ListenActor) runListen(ctx context.Context) {
	first := true
	events := peoplecounting.Listen(ctx, act.socket, act.errLog, act.warnLog, act.cameralog)
	for v := range events {
		act.buildLog.Printf("listen event: %#v\n", v)
		id := act.doorID(v.ID)
		fmt.Printf("id: %v\n", id)
		switch event := v.Data.(type) {
		case *peoplecounting.EventNotificationAlertPeopleConting:
			// Only "realTime" carries the camera's accumulated counters. Both
			// "timeRange" and "signalTrigger" report the count of a time window
			// instead (measured: enter=1 arrives while the accumulated value is 17),
			// so feeding them to CountingActor yields a large negative delta, adds
			// the window count when it is below 4 and leaves rawXmap at that small
			// value, which then discards the next real event as a jump over 10.
			// statisticalMethods is optional in ISAPI, so an empty value is taken as
			// realTime rather than dropping every event from such a camera.
			if m := event.PeopleCounting.StatisticalMethods; strings.Contains(m, "timeRange") ||
				strings.Contains(m, "signalTrigger") {
				act.warnLog.Printf("event %s (window count, not accumulated), events -> %+v",
					m, event.PeopleCounting)
				break
			}
			dateTime, err := parseDateTime(event.DateTime)
			if err != nil {
				act.warnLog.Printf("time event error -> %s", err)
				break
			}
			// A small step back is an out of order delivery and is dropped. A large
			// one is the camera clock being corrected (NTP sync, or a reboot with a
			// default date): rejecting those would silently discard every event of
			// that door until its clock passed the old mark again, which with a
			// synchronizeInterval measured in hours means losing a whole shift.
			if back := act.timeBefore[id].Sub(dateTime); back > 0 {
				if back < resyncThreshold {
					act.warnLog.Printf("time event error, events in the past -> new %v, before %v",
						dateTime, act.timeBefore[id])
					break
				}
				act.warnLog.Printf("camera (id: %d) clock stepped back %v, taking %v as the new reference",
					id, back, dateTime)
				// Los recortes de video pendientes de esta puerta quedan inservibles:
				// su marca de tiempo ya no apunta a la misma posición de la grabación.
				act.context.Send(act.countingActor, &MsgClockStep{ID: int32(id)})
			}
			act.timeBefore[id] = dateTime

			act.cameralog.Printf("%d: listen event: %+v\n", time.Now().UnixNano()/1000_000, event)
			act.cameralog.Printf("%d: listen event: %+v\n", time.Now().UnixNano()/1000_000, event.PeopleCounting)
			if first {
				act.infoLog.Printf("initial event -> %+v", event.PeopleCounting)
				act.infoLog.Printf("initial event -> %+v", event)
				first = false
			}
			enters := event.PeopleCounting.Enter
			if diff := enters - act.entersBefore[id]; diff > 0 {
				act.context.Send(act.countingActor, &messages.Event{ID: int32(id), Type: messages.Event_INPUT,
					Value: enters, Timestamp: dateTime.Unix()})
			}
			act.entersBefore[id] = enters
			exits := event.PeopleCounting.Exit
			if diff := exits - act.exitsBefore[id]; diff > 0 {
				act.context.Send(act.countingActor, &messages.Event{ID: int32(id), Type: messages.Event_OUTPUT,
					Value: exits, Timestamp: dateTime.Unix()})
			}
			act.exitsBefore[id] = exits
		case *peoplecounting.EventNotificationAlertCounting:
			// Conteo por ventana de la G1. Enter/Exit YA son el incremento, asi que van
			// marcados con Delta y CountingActor no resta contra nada.
			//
			// La marca de tiempo del evento es el FIN de la ventana. Se usa esa y no el
			// inicio porque es la unica que acota de verdad cuando ocurrieron los cruces
			// —estan en algun punto del intervalo— y porque el reloj que importa para el
			// video es el de la camara en el momento que reporta.
			dateTime, err := parseDateTime(event.DateTime)
			if err != nil {
				act.warnLog.Printf("time event error -> %s", err)
				break
			}
			act.timeBefore[id] = dateTime
			act.cameralog.Printf("%d: listen event (ventana): %+v\n",
				time.Now().UnixNano()/1000_000, event.Counting)

			// El conteo por ventana no sirve para recortar video: los cruces ocurrieron
			// en algun momento de un intervalo de minutos, no en el instante del evento.
			// Pedir un clip centrado aca mostraria otra cosa. Se avisa una sola vez por
			// puerta para no repetirlo en cada ventana.
			if !act.ventanaAvisada[id] {
				act.ventanaAvisada[id] = true
				act.warnLog.Printf("camara (id: %d) reporta el conteo por VENTANA [%s, %s], "+
					"no por cruce: los pasos se cuentan pero no se les puede extraer video, "+
					"porque no se sabe en que instante del intervalo ocurrieron",
					id, event.Counting.TimeRange.StartTime, event.Counting.TimeRange.EndTime)
			}

			if n := event.Counting.Enter; n > 0 {
				act.context.Send(act.countingActor, &messages.Event{ID: int32(id),
					Type: messages.Event_INPUT, Value: n, Timestamp: dateTime.Unix(), Delta: true})
			}
			if n := event.Counting.Exit; n > 0 {
				act.context.Send(act.countingActor, &messages.Event{ID: int32(id),
					Type: messages.Event_OUTPUT, Value: n, Timestamp: dateTime.Unix(), Delta: true})
			}
		case *peoplecounting.EventNotificationAlert:
			switch event.EventType {
			case peoplecounting.ScenechangedetectionType:
				act.context.Send(act.countingActor, &messages.Event{ID: int32(id), Type: messages.Event_TAMPERING, Value: 0})
			case peoplecounting.ShelteralarmType:
				act.context.Send(act.countingActor, &messages.Event{ID: int32(id), Type: messages.Event_TAMPERING, Value: 0})
			}
		}
	}
	act.context.Send(act.context.Self(), &msgListenError{})
}
