package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/isapi"
	"github.com/dumacp/gpsnmea"
	"github.com/dumacp/pubsub"
)

const (
	// cameraFirstDelay espera antes del primer ciclo. Al arrancar el equipo compiten el
	// broker, la red y la propia cámara; preguntarle de inmediato solo produce un error
	// que no significa nada.
	cameraFirstDelay = 90 * time.Second
	// cameraHTTPTimeout acota cada diálogo con una cámara.
	cameraHTTPTimeout = 15 * time.Second
	// cameraDriftCycles son los ciclos consecutivos con deriva que hacen falta para
	// alarmar. Con uno solo, un pico de latencia o una corrección en curso generaría
	// falsas alarmas.
	cameraDriftCycles = 3
)

// NTPConfig es la configuración de hora que se quiere en toda la flota.
type NTPConfig struct {
	Server string
	Port   int
	// Interval es cada cuánto la cámara sincroniza. La API lo expresa en minutos.
	Interval time.Duration
	// TimeZone es la cadena POSIX. Ojo con el signo, que está invertido: "CST+5:00:00"
	// significa UTC-5, que es lo correcto para Colombia. Ponerlo como "-5:00:00" deja la
	// cámara diez horas corrida.
	TimeZone string
	// DriftMax es la deriva tolerada antes de alarmar.
	DriftMax time.Duration
}

// CameraActor mantiene la configuración de hora de las cámaras y avisa cuando una deriva.
//
// Corrige la configuración —modo NTP, servidor, intervalo, zona— pero NUNCA escribe el
// reloj. Escribirlo obliga a pasar por timeMode "manual" y a restaurar NTP después, y un
// salto de reloj hacia atrás es justo lo que hace que ListenActor descarte eventos. Ante
// una deriva que persiste se alarma y se deja que la corrija el NTP de la cámara.
type CameraActor struct {
	*Logger
	cameras  []string
	user     string
	pass     string
	want     NTPConfig
	interval time.Duration

	state   map[int32]*camTimeState
	working bool
	cancel  func()
}

// camTimeState recuerda lo justo para no repetir logs ni alarmas en cada ciclo.
type camTimeState struct {
	serial string
	// driftCycles cuenta ciclos consecutivos fuera de tolerancia.
	driftCycles int
	alerted     bool
	// unauthorized deja de intentar: la cámara bloquea el usuario tras varios fallos y
	// reintentar cada ciclo con una credencial mala la deja inaccesible en campo.
	unauthorized bool
}

// msgCameraTick dispara un ciclo de revisión.
type msgCameraTick struct{}

// msgCameraResult trae el resultado de una cámara desde la goroutine al actor.
type msgCameraResult struct {
	door   int32
	host   string
	serial string
	// fixed describe lo que se corrigió, vacío si no hubo cambios.
	fixed []string
	// drift es la deriva medida; válida solo si driftOK.
	drift   time.Duration
	driftOK bool
	// ntpReachable indica el resultado del test del servidor NTP; solo se consulta
	// cuando hay deriva, para no molestar a la cámara en cada ciclo.
	ntpTested    bool
	ntpReachable bool
	ntpError     string
	// mode, server e interval son lo observado, para el evento y el log.
	mode           string
	server         string
	interval       int
	rebootRequired bool
	err            error
	unauthorized   bool
}

// NewCameraActor crea el actor.
func NewCameraActor(cameras []string, user, pass string, want NTPConfig, interval time.Duration) *CameraActor {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	a := &CameraActor{
		cameras:  cameras,
		user:     user,
		pass:     pass,
		want:     want,
		interval: interval,
		state:    make(map[int32]*camTimeState),
	}
	a.Logger = &Logger{}
	return a
}

// Receive func Receive in actor
func (a *CameraActor) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		a.initLogs()
		a.infoLog.Printf("actor started \"%s\": ntp=%s:%d cada %v, zona %q, deriva máxima %v, ciclo %v",
			ctx.Self().Id, a.want.Server, a.want.Port, a.want.Interval,
			a.want.TimeZone, a.want.DriftMax, a.interval)
		a.schedule(ctx, cameraFirstDelay)

	case *actor.Stopping:
		a.warnLog.Println("stopped actor")
		if a.cancel != nil {
			a.cancel()
		}

	case *msgCameraTick:
		a.runCycle(ctx)

	case *msgCameraResult:
		a.handleResult(ctx, msg)
	}
}

// schedule programa el próximo ciclo.
func (a *CameraActor) schedule(ctx actor.Context, d time.Duration) {
	self, system := ctx.Self(), ctx.ActorSystem()
	time.AfterFunc(d, func() { system.Root.Send(self, &msgCameraTick{}) })
}

// runCycle revisa todas las cámaras en una goroutine. El diálogo HTTP nunca ocurre dentro
// de Receive: una cámara que no responde bloquearía el actor durante el timeout.
func (a *CameraActor) runCycle(ctx actor.Context) {
	a.schedule(ctx, a.interval)
	if a.working {
		a.warnLog.Println("el ciclo anterior todavía corre, se omite este")
		return
	}

	// La referencia horaria se pide antes de lanzar la goroutine, porque va por el padre
	// y eso solo se puede hacer desde el hilo del actor.
	ref, refFuente := a.reference(ctx)

	trabajos := make([]struct {
		door int32
		host string
	}, 0, len(a.cameras))
	for i, host := range a.cameras {
		if len(host) == 0 {
			continue
		}
		if st := a.state[int32(i)]; st != nil && st.unauthorized {
			continue
		}
		trabajos = append(trabajos, struct {
			door int32
			host string
		}{int32(i), host})
	}
	if len(trabajos) == 0 {
		return
	}

	a.working = true
	self, system := ctx.Self(), ctx.ActorSystem()
	cctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(len(trabajos))*4*cameraHTTPTimeout)
	a.cancel = cancel

	a.buildLog.Printf("ciclo de hora sobre %d cámara(s), referencia %s", len(trabajos), refFuente)

	go func() {
		defer cancel()
		for _, t := range trabajos {
			res := a.checkCamera(cctx, t.door, t.host, ref)
			system.Root.Send(self, res)
		}
		// Una marca final para liberar el candado, con door negativo.
		system.Root.Send(self, &msgCameraResult{door: -1})
	}()
}

// reference devuelve la hora de referencia y de dónde salió.
//
// Se prefiere el GPS: es independiente de la red y del reloj del gateway, que es
// justamente lo que se está auditando. Si no hay trama válida se usa el reloj local, que
// alcanza para detectar una deriva de minutos.
func (a *CameraActor) reference(ctx actor.Context) (time.Time, string) {
	if ctx.Parent() == nil {
		return time.Now(), "reloj del equipo"
	}
	res, err := ctx.RequestFuture(ctx.Parent(), &MsgGetGps{}, 500*time.Millisecond).Result()
	if err != nil {
		return time.Now(), "reloj del equipo (GPS no respondió)"
	}
	datagps, ok := res.(*MsgGPS)
	if !ok || len(datagps.Data) == 0 {
		return time.Now(), "reloj del equipo (sin trama GPS)"
	}
	t, err := gpsTime(string(datagps.Data))
	if err != nil {
		return time.Now(), "reloj del equipo (" + err.Error() + ")"
	}
	return t, "GPS"
}

// gpsTime arma la hora UTC de una trama $GPRMC.
func gpsTime(frame string) (time.Time, error) {
	v := gpsnmea.ParseRMC(frame)
	if v == nil {
		return time.Time{}, errors.New("trama GPS ilegible")
	}
	if !v.Validity {
		return time.Time{}, errors.New("trama GPS sin fix")
	}
	// TimeStamp viene como hhmmss.sss y DateStamp como ddmmyy, los dos en UTC.
	hhmmss := v.TimeStamp
	if i := strings.IndexByte(hhmmss, '.'); i > 0 {
		hhmmss = hhmmss[:i]
	}
	if len(hhmmss) != 6 || len(v.DateStamp) != 6 {
		return time.Time{}, errors.New("hora GPS con formato inesperado")
	}
	t, err := time.ParseInLocation("020106150405", v.DateStamp+hhmmss, time.UTC)
	if err != nil {
		return time.Time{}, errors.New("hora GPS inválida")
	}
	return t, nil
}

// checkCamera revisa y corrige una cámara. No escribe el reloj nunca.
func (a *CameraActor) checkCamera(ctx context.Context, door int32, host string, ref time.Time) *msgCameraResult {
	res := &msgCameraResult{door: door, host: host}
	cli := isapi.New(host, a.user, a.pass, cameraHTTPTimeout)

	// Identidad, para que el evento diga de qué equipo habla.
	if info, err := cli.GetDeviceInfo(ctx); err == nil {
		res.serial = info.SerialNumber
	}

	antes := time.Now()
	tm, err := cli.GetTime(ctx)
	rtt := time.Since(antes)
	if err != nil {
		res.err = err
		res.unauthorized = errors.Is(err, isapi.ErrUnauthorized)
		return res
	}
	res.mode = tm.TimeMode

	// Deriva contra la referencia, corrigiendo por la mitad del viaje ida y vuelta: sin
	// eso el RTT se confunde con deriva.
	if local, err := tm.Parsed(); err == nil {
		refAjustada := ref.Add(rtt / 2)
		res.drift = local.Sub(refAjustada)
		res.driftOK = true
	}

	// Configuración de hora: solo se escribe si difiere.
	necesitaModo := !strings.EqualFold(tm.TimeMode, "NTP")
	necesitaZona := len(a.want.TimeZone) > 0 && tm.TimeZone != a.want.TimeZone
	if necesitaModo || necesitaZona {
		nuevo := *tm
		nuevo.TimeMode = "NTP"
		if len(a.want.TimeZone) > 0 {
			nuevo.TimeZone = a.want.TimeZone
		}
		// LocalTime solo es válido con manual o local; enviarlo con NTP puede hacer que
		// la cámara rechace el mensaje.
		nuevo.LocalTime = ""
		if err := cli.PutTime(ctx, &nuevo); err != nil {
			if rebootRequired(err) {
				res.rebootRequired = true
				res.fixed = append(res.fixed, describeTimeFix(necesitaModo, necesitaZona, tm, a.want))
			} else {
				res.err = fmt.Errorf("corrigiendo la hora: %w", err)
				res.unauthorized = errors.Is(err, isapi.ErrUnauthorized)
				return res
			}
		} else {
			res.fixed = append(res.fixed, describeTimeFix(necesitaModo, necesitaZona, tm, a.want))
		}
	}

	// Servidores NTP.
	lista, err := cli.GetNTPServers(ctx)
	if err != nil {
		res.err = fmt.Errorf("leyendo los servidores NTP: %w", err)
		res.unauthorized = errors.Is(err, isapi.ErrUnauthorized)
		return res
	}
	var actual *isapi.NTPServer
	if len(lista.Servers) > 0 {
		actual = &lista.Servers[0]
		res.server = actual.Address()
		res.interval = actual.SynchronizeInterval
	}
	if deseado := a.wantedServer(lista); deseado != nil {
		if err := cli.PutNTPServers(ctx, deseado); err != nil {
			if rebootRequired(err) {
				res.rebootRequired = true
				res.fixed = append(res.fixed, a.describeNTPFix(actual))
			} else {
				res.err = fmt.Errorf("corrigiendo los servidores NTP: %w", err)
				res.unauthorized = errors.Is(err, isapi.ErrUnauthorized)
				return res
			}
		} else {
			res.fixed = append(res.fixed, a.describeNTPFix(actual))
			res.server = a.want.Server
			res.interval = int(a.want.Interval.Minutes())
		}
	}

	// El test del servidor solo cuando hay deriva: distingue "problema de red" de
	// "problema de reloj", y no hay razón para molestar a la cámara si todo está en hora.
	if res.driftOK && abs(res.drift) > a.want.DriftMax && len(res.server) > 0 {
		desc := &isapi.NTPTestDescription{PortNo: a.want.Port}
		if actual != nil {
			desc.AddressingFormatType = actual.AddressingFormatType
			desc.HostName = actual.HostName
			desc.IPAddress = actual.IPAddress
			if actual.PortNo > 0 {
				desc.PortNo = actual.PortNo
			}
		}
		if len(desc.AddressingFormatType) == 0 {
			desc.AddressingFormatType = "hostname"
			desc.HostName = a.want.Server
		}
		res.ntpTested = true
		if r, err := cli.TestNTPServer(ctx, desc); err != nil {
			res.ntpError = err.Error()
		} else {
			res.ntpReachable = r.Reachable()
			res.ntpError = r.ErrorDescription
		}
	}
	return res
}

// wantedServer devuelve la lista a escribir si la actual difiere, o nil si ya está bien.
func (a *CameraActor) wantedServer(actual *isapi.NTPServerList) *isapi.NTPServerList {
	if len(a.want.Server) == 0 {
		return nil
	}
	quiero := isapi.NTPServer{
		ID:                   "1",
		AddressingFormatType: "hostname",
		HostName:             a.want.Server,
		PortNo:               a.want.Port,
		SynchronizeInterval:  int(a.want.Interval.Minutes()),
	}
	if len(actual.Servers) == 1 {
		s := actual.Servers[0]
		if s.Address() == quiero.HostName && s.PortNo == quiero.PortNo &&
			s.SynchronizeInterval == quiero.SynchronizeInterval {
			return nil
		}
	}
	// Se conserva el sobre del dispositivo para reenviar su propio namespace, que varía
	// entre modelos.
	out := &isapi.NTPServerList{Version: actual.Version, Xmlns: actual.Xmlns}
	out.Servers = []isapi.NTPServer{quiero}
	return out
}

func (a *CameraActor) describeNTPFix(actual *isapi.NTPServer) string {
	if actual == nil {
		return fmt.Sprintf("NTP configurado en %s:%d cada %d min (no había servidor)",
			a.want.Server, a.want.Port, int(a.want.Interval.Minutes()))
	}
	return fmt.Sprintf("NTP %s:%d cada %d min -> %s:%d cada %d min",
		actual.Address(), actual.PortNo, actual.SynchronizeInterval,
		a.want.Server, a.want.Port, int(a.want.Interval.Minutes()))
}

func describeTimeFix(modo, zona bool, tm *isapi.Time, want NTPConfig) string {
	var partes []string
	if modo {
		partes = append(partes, fmt.Sprintf("timeMode %s -> NTP", tm.TimeMode))
	}
	if zona {
		partes = append(partes, fmt.Sprintf("timeZone %q -> %q", tm.TimeZone, want.TimeZone))
	}
	return strings.Join(partes, ", ")
}

// handleResult aplica el resultado: log solo de lo que cambió y alarma si corresponde.
func (a *CameraActor) handleResult(ctx actor.Context, res *msgCameraResult) {
	if res.door < 0 {
		a.working = false
		return
	}
	st := a.state[res.door]
	if st == nil {
		st = &camTimeState{}
		a.state[res.door] = st
	}
	if len(res.serial) > 0 {
		st.serial = res.serial
	}

	if res.unauthorized {
		st.unauthorized = true
		// Se deja de consultar esta cámara hasta el próximo arranque: la cámara bloquea
		// el usuario tras varios fallos, así que insistir cada ciclo con una credencial
		// mala la dejaría inaccesible en campo.
		a.errLog.Printf("cámara de la puerta %d (%s) rechazó las credenciales: se deja de "+
			"consultar. Corregí HIKVISION_CREDENTIALS y reiniciá el binario",
			res.door, res.host)
		return
	}
	if res.err != nil {
		a.warnLog.Printf("cámara de la puerta %d (%s): %s", res.door, res.host, res.err)
		return
	}

	for _, f := range res.fixed {
		a.infoLog.Printf("cámara de la puerta %d (%s) corregida: %s", res.door, res.host, f)
	}
	if res.rebootRequired {
		a.warnLog.Printf("cámara de la puerta %d (%s) pide reinicio para aplicar el cambio; "+
			"no se reinicia sola, decidilo vos", res.door, res.host)
	}

	if !res.driftOK {
		a.buildLog.Printf("cámara de la puerta %d: no se pudo medir la deriva", res.door)
		return
	}

	fuera := abs(res.drift) > a.want.DriftMax
	switch {
	case fuera:
		st.driftCycles++
		if st.driftCycles >= cameraDriftCycles && !st.alerted {
			st.alerted = true
			a.warnLog.Printf("cámara de la puerta %d (%s) derivada %+.1fs durante %d ciclos; "+
				"servidor NTP alcanzable=%v (%s)",
				res.door, res.host, res.drift.Seconds(), st.driftCycles, res.ntpReachable, res.ntpError)
			a.publish(ctx, res, st, "drift")
		} else {
			a.buildLog.Printf("cámara de la puerta %d derivada %+.1fs (ciclo %d de %d)",
				res.door, res.drift.Seconds(), st.driftCycles, cameraDriftCycles)
		}
	default:
		if st.alerted {
			a.infoLog.Printf("cámara de la puerta %d (%s) volvió a hora, deriva %+.1fs",
				res.door, res.host, res.drift.Seconds())
			a.publish(ctx, res, st, "ok")
		}
		st.driftCycles = 0
		st.alerted = false
		a.buildLog.Printf("cámara de la puerta %d en hora: deriva %+.1fs, modo %s, ntp %s cada %d min",
			res.door, res.drift.Seconds(), res.mode, res.server, res.interval)
	}

	if len(res.fixed) > 0 {
		a.publish(ctx, res, st, "fixed")
	}
}

// publish avisa a la plataforma. Se publica solo en los cambios de estado: una línea por
// ciclo por cámara sería ruido que esconde lo que importa.
func (a *CameraActor) publish(ctx actor.Context, res *msgCameraResult, st *camTimeState, estado string) {
	if ctx.Parent() == nil {
		return
	}
	val := struct {
		ID           int32    `json:"id"`
		Type         string   `json:"type"`
		Status       string   `json:"status"`
		Camera       string   `json:"camera"`
		CameraSerial string   `json:"camera_serial,omitempty"`
		DriftS       float64  `json:"drift_s"`
		TimeMode     string   `json:"time_mode,omitempty"`
		NTPServer    string   `json:"ntp_server,omitempty"`
		NTPIntervalM int      `json:"ntp_interval_min,omitempty"`
		NTPReachable *bool    `json:"ntp_reachable,omitempty"`
		Fixed        []string `json:"fixed,omitempty"`
	}{
		ID: res.door, Type: "CAMERA", Status: estado,
		Camera: res.host, CameraSerial: st.serial,
		DriftS: round1(res.drift.Seconds()), TimeMode: res.mode,
		NTPServer: res.server, NTPIntervalM: res.interval,
		Fixed: res.fixed,
	}
	if res.ntpTested {
		v := res.ntpReachable
		val.NTPReachable = &v
	}
	data, err := json.Marshal(&pubsub.Message{
		Timestamp: float64(time.Now().UnixNano()) / 1000000000,
		Type:      "CAMERATIME",
		Value:     val,
	})
	if err != nil {
		a.errLog.Printf("armando CAMERATIME: %s", err)
		return
	}
	a.buildLog.Printf("%s", data)
	ctx.Send(ctx.Parent(), &msgAddEvent{data: data})
}

// rebootRequired indica si el dispositivo aplicó el cambio pero pide reinicio.
//
// La cámara responde statusCode 7 en ese caso, y el cliente isapi lo trata como error
// porque OK() solo acepta 0 y 1. Acá se interpreta como "aplicado, avisar": reiniciar una
// cámara es disruptivo y no es una decisión que deba tomar un binario solo.
func rebootRequired(err error) bool {
	var st *isapi.ResponseStatus
	return errors.As(err, &st) && st.StatusCode == 7
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5*sign(f))) / 10
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}
