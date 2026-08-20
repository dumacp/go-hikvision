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
	// encoderMinUptime es lo que el binario tiene que llevar encendido antes de tocar el
	// perfil de codificación.
	//
	// Es el único freno que tiene el reinicio de cámara, y reemplaza a la ventana horaria
	// que había antes. La ventana no servía: el gateway se alimenta del vehículo y de noche
	// queda apagado, así que una franja de madrugada nunca la alcanzaba ningún ciclo y el
	// ajuste quedaba sin aplicar para siempre.
	//
	// Lo que sí hay que acotar es el bucle: el tope de "un reinicio por cámara" vive en
	// memoria y se pierde al reiniciar el binario, y este repo tiene historia de bucles de
	// supervisión (el bug de ctx.Parent() nulo produjo 7 arranques seguidos). Un binario que
	// se reinicia en bucle nunca llega a este mínimo, así que nunca reinicia una cámara.
	encoderMinUptime = 30 * time.Minute
)

// CameraConfig es la configuración que se quiere en toda la flota de cámaras.
type CameraConfig struct {
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

	// RecordStart y RecordEnd son la ventana de grabación deseada, en HH:MM:SS de la hora
	// local de la cámara. Vacías dejan el horario como esté.
	//
	// Importa porque un evento fuera de la ventana no tiene video posible: la cámara de
	// laboratorio venía con 07:55-16:02 y dejaba media jornada sin nada que extraer. Y no
	// conviene poner 24/7 sin pensar: con 507 kbps medidos, una SD de 8 GB da 1.4 días de
	// retención grabando todo el día contra 1.7 grabando veinte horas. La palanca real de
	// la retención es el tamaño de la tarjeta, no el horario.
	RecordStart string
	RecordEnd   string

	// SmartCodec pide "on", "off" o vacío para dejarlo como esté. Es el ajuste que más
	// importa para el video: con H.264+ activo y escena quieta la cámara graba un keyframe
	// cada varios segundos y el clip parece congelado. Medido en la cámara de laboratorio:
	// 49 frames con un hueco de 5.7 s contra 201 continuos al desactivarlo.
	SmartCodec string
	// FrameRate en fps y GopFrames en cuadros; cero deja el valor como esté. ISAPI guarda
	// los fps en centi-fps, la conversión la hace el actor.
	FrameRate int
	GopFrames int
}

// wantsEncoder indica si hay algo que alinear en el perfil de codificación.
func (c CameraConfig) wantsEncoder() bool {
	return len(c.SmartCodec) > 0 || c.FrameRate > 0 || c.GopFrames > 0
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
	want     CameraConfig
	interval time.Duration

	state   map[int32]*camTimeState
	working bool
	cancel  func()
	// startedAt fija el arranque, para el mínimo de encoderMinUptime.
	startedAt time.Time
}

// camTimeState recuerda lo justo para no repetir logs ni alarmas en cada ciclo.
type camTimeState struct {
	serial string
	// driftCycles cuenta ciclos consecutivos fuera de tolerancia.
	driftCycles int
	alerted     bool
	// storageAlerted evita repetir la alarma del almacenamiento en cada ciclo.
	storageAlerted bool
	// unauthorized deja de intentar: la cámara bloquea el usuario tras varios fallos y
	// reintentar cada ciclo con una credencial mala la deja inaccesible en campo.
	unauthorized bool
	// rebooted limita a UN reinicio por cámara por arranque del binario.
	//
	// Sin este tope, un modelo que acepta el PUT pero no lo persiste dejaría el ciclo
	// viendo la misma diferencia para siempre: escribir, statusCode 7, reiniciar, cada
	// media hora y en toda la flota. Un reinicio que no arregla nada es un problema para
	// mirar en el log, no algo para repetir solo.
	rebooted bool
	// rebootWarned evita repetir en cada ciclo el aviso de "hay cambios sin aplicar".
	rebootWarned bool
	// encoderGiveUp deja de escribir el encoder de esta cámara hasta el próximo arranque,
	// porque un valor pedido no quedó guardado y reintentarlo es un PUT por ciclo para
	// siempre. Hay que corregir el flag, no insistir.
	encoderGiveUp bool
}

// msgCameraTick dispara un ciclo de revisión.
type msgCameraTick struct{}

// msgRebootDone informa el resultado de un reinicio pedido a una cámara.
type msgRebootDone struct {
	door int32
	host string
	err  error
}

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

	// storage describe el medio de grabación. storageOK falso es la condición que hace
	// imposible extraer cualquier clip, así que se reporta aparte de los errores.
	storageOK      bool
	storageStatus  string
	storageFreeMB  int
	storageChecked bool

	// codec describe lo observado en el perfil de codificación. codecType se reporta y
	// nunca se escribe: video/extract.go solo sabe H.264, así que cambiarlo a H.265 desde
	// acá rompería la extracción en silencio; que quede en el evento y lo decida un humano.
	codecChecked bool
	codecType    string
	codecFPS     float64
	smartCodec   bool
	// encoderErr se reporta aparte de err: un fallo acá no invalida lo demás del ciclo.
	encoderErr error
	// encoderPending son las diferencias medidas y NO escritas todavía, porque el binario
	// aún no llegó a encoderMinUptime. Sirven para avisar qué falta aplicar.
	encoderPending []string
	// encoderStuck son los campos que se escribieron con OK y NO quedaron guardados: la
	// cámara recortó el valor en silencio. Sin esto el ciclo reescribiría para siempre.
	encoderStuck  []string
	stuckObserved string
}

// NewCameraActor crea el actor.
func NewCameraActor(cameras []string, user, pass string, want CameraConfig, interval time.Duration) *CameraActor {
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
		a.startedAt = time.Now()
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

	case *msgRebootDone:
		// Un error acá no reabre el candado: si el PUT falló pero la cámara igual se
		// reinició, insistir la reiniciaría dos veces. Queda en el log y se revisa.
		if msg.err != nil {
			a.errLog.Printf("no se pudo reiniciar la cámara de la puerta %d (%s): %s; "+
				"el cambio queda pendiente hasta el próximo arranque del binario",
				msg.door, msg.host, msg.err)
			return
		}
		a.infoLog.Printf("cámara de la puerta %d (%s) reiniciada; vuelve a enviar eventos "+
			"cuando termine de arrancar", msg.door, msg.host)
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

	// El perfil de codificación se decide acá, en el hilo del actor, y no en la goroutine.
	//
	// Se escribe siempre que haya algo que alinear, con dos condiciones: que el binario lleve
	// encendido al menos encoderMinUptime, y que esta cámara no haya rechazado ya un valor.
	// El PUT y el reinicio quedan en el mismo ciclo, segundos aparte, porque un cambio que
	// responde statusCode 7 queda **guardado pero inerte y la cámara reporta el valor
	// guardado, no el efectivo**: si se escribiera ahora y el reinicio quedara para después,
	// un arranque del binario en el medio haría que el ciclo siguiente no viera diferencia y
	// nadie reiniciara nunca.
	encoderListo := a.want.wantsEncoder() && time.Since(a.startedAt) >= encoderMinUptime

	trabajos := make([]struct {
		door    int32
		host    string
		encoder bool
	}, 0, len(a.cameras))
	for i, host := range a.cameras {
		if len(host) == 0 {
			continue
		}
		st := a.state[int32(i)]
		if st != nil && st.unauthorized {
			continue
		}
		trabajos = append(trabajos, struct {
			door    int32
			host    string
			encoder bool
		}{int32(i), host, encoderListo && (st == nil || !st.encoderGiveUp)})
	}
	if len(trabajos) == 0 {
		return
	}

	a.working = true
	self, system := ctx.Self(), ctx.ActorSystem()
	cctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(len(trabajos))*5*cameraHTTPTimeout)
	a.cancel = cancel

	a.buildLog.Printf("ciclo de hora sobre %d cámara(s), referencia %s (encoder listo=%v)",
		len(trabajos), refFuente, encoderListo)

	go func() {
		defer cancel()
		for _, t := range trabajos {
			res := a.checkCamera(cctx, t.door, t.host, ref, t.encoder)
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
func (a *CameraActor) checkCamera(ctx context.Context, door int32, host string, ref time.Time,
	escribirEncoder bool) *msgCameraResult {
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

	// Horario de grabación. Se aplica en caliente: verificado que responde statusCode 1 y
	// queda activo sin reiniciar, a diferencia de la configuración del encoder.
	if len(a.want.RecordStart) > 0 && len(a.want.RecordEnd) > 0 {
		if raw, err := cli.GetTrackRaw(ctx, videoTrack); err != nil {
			res.err = fmt.Errorf("leyendo el horario de grabación: %w", err)
			res.unauthorized = errors.Is(err, isapi.ErrUnauthorized)
			return res
		} else if nuevo, cambios, err := isapi.SetScheduleWindow(raw, a.want.RecordStart, a.want.RecordEnd); err != nil {
			res.err = fmt.Errorf("armando el horario: %w", err)
			return res
		} else if cambios > 0 {
			if err := cli.PutTrackRaw(ctx, videoTrack, nuevo); err != nil {
				res.err = fmt.Errorf("escribiendo el horario de grabación: %w", err)
				res.unauthorized = errors.Is(err, isapi.ErrUnauthorized)
				return res
			}
			res.fixed = append(res.fixed, fmt.Sprintf("horario de grabación -> %s a %s (%d ventana(s))",
				a.want.RecordStart, a.want.RecordEnd, cambios))
		}
	}

	// Almacenamiento. No se formatea nunca: borra todo lo grabado y esa no es una decisión
	// para un binario, igual que el reinicio. Solo se reporta, porque una SD sin formatear
	// hace que ningún clip se pueda extraer y sin este aviso se culparía al extractor.
	if st, err := cli.GetStorage(ctx); err == nil && len(st.HDDs) > 0 {
		h := st.HDDs[0]
		res.storageChecked = true
		res.storageOK = h.Healthy()
		res.storageStatus = h.Status
		res.storageFreeMB = h.FreeSpace
	}

	// Perfil de codificación. Va al final a propósito: es el único cambio que exige
	// reiniciar, así que si algo falló antes no se llega a pedir un reinicio por una cámara
	// que además tiene otro problema.
	if a.want.wantsEncoder() {
		a.checkEncoder(ctx, cli, res, escribirEncoder)
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

// checkEncoder alinea el perfil de codificación del canal principal.
//
// Con escribir en falso solo mide la diferencia y la deja en res.encoderPending: es lo que
// pasa durante los primeros encoderMinUptime del binario.
//
// Un error acá NO aborta el ciclo: la hora, el NTP, el horario y el almacenamiento ya
// quedaron revisados, y el perfil es lo menos urgente de los cinco. Se anota como
// advertencia en el resultado y sigue.
func (a *CameraActor) checkEncoder(ctx context.Context, cli *isapi.Client, res *msgCameraResult,
	escribir bool) {
	// La lectura tipada es solo para reportar; los cambios van sobre el XML crudo, porque
	// el documento trae transporte, multicast, audio y overlays, y reconstruirlo desde un
	// struct con los campos que interesan borraría todo lo demás.
	if enc, err := cli.GetChannelEncoder(ctx, videoTrack); err == nil {
		res.codecChecked = true
		res.codecType = enc.Video.CodecType
		res.codecFPS = enc.FrameRate()
		res.smartCodec = enc.Video.SmartCodec.Enabled
	}

	raw, err := cli.GetChannelRaw(ctx, videoTrack)
	if err != nil {
		res.encoderErr = fmt.Errorf("leyendo el perfil de codificación: %w", err)
		res.unauthorized = res.unauthorized || errors.Is(err, isapi.ErrUnauthorized)
		return
	}

	nuevo, cambios, err := a.encoderDiff(raw)
	if err != nil {
		res.encoderErr = err
		return
	}
	if len(cambios) == 0 {
		return
	}
	if !escribir {
		res.encoderPending = cambios
		return
	}

	if err := cli.PutChannelRaw(ctx, videoTrack, nuevo); err != nil {
		if !rebootRequired(err) {
			res.encoderErr = fmt.Errorf("escribiendo el perfil de codificación: %w", err)
			res.unauthorized = res.unauthorized || errors.Is(err, isapi.ErrUnauthorized)
			return
		}
		// statusCode 7 no es un fallo: el cambio quedó guardado y hace falta reiniciar
		// para que tome efecto. Medido, y depende del CAMPO, no del endpoint: GovLength y
		// maxFrameRate responden 1 y se aplican en caliente; SmartCodec responde 7.
		res.rebootRequired = true
	}

	// Verificar que el valor quedó guardado, porque un OK no lo garantiza.
	//
	// Medido en el DS-2XM6825G0: pedirle 30 fps —que no está en su lista de valores
	// admitidos— responde statusCode 1 OK y guarda 24 **en silencio**. Sin esta
	// comprobación el ciclo siguiente vuelve a ver la diferencia, escribe otra vez, y
	// queda un PUT por cámara cada media hora para siempre, con una línea de log que dice
	// "corregida" sin que nada se corrija. Eso rompe la idempotencia, que es la propiedad
	// sobre la que está construido todo esto.
	//
	// La comprobación es genérica a propósito: no valida contra la lista de capacidades
	// —que solo existe para algunos campos— sino que relee y vuelve a medir la diferencia,
	// así atrapa cualquier recorte silencioso, de este modelo o de otro.
	//
	// Ojo con el statusCode 7: ahí la cámara reporta el valor GUARDADO, que es el que se
	// pidió, así que la relectura no da falso positivo aunque el cambio esté inerte.
	verificar, err := cli.GetChannelRaw(ctx, videoTrack)
	if err != nil {
		// No se pudo verificar. Se declara aplicado, que es lo que dijo la cámara, y el
		// ciclo siguiente vuelve a medir.
		res.fixed = append(res.fixed, "codificación: "+strings.Join(cambios, ", "))
		return
	}
	_, restantes, err := a.encoderDiff(verificar)
	if err != nil || len(restantes) == 0 {
		res.fixed = append(res.fixed, "codificación: "+strings.Join(cambios, ", "))
		return
	}
	// No quedó guardado, así que NO se declara corregido: decir "fixed" de algo que la
	// cámara no aplicó es mentirle a la plataforma.
	res.encoderStuck = restantes
	if enc, err := cli.GetChannelEncoder(ctx, videoTrack); err == nil {
		res.stuckObserved = fmt.Sprintf("%.0f fps, GOP %d, SmartCodec %v",
			enc.FrameRate(), enc.Video.GovLength, enc.Video.SmartCodec.Enabled)
	}
}

// encoderDiff calcula el XML a escribir y describe qué cambia. Sin cambios devuelve una
// lista vacía, que es lo que hace idempotente al ciclo.
func (a *CameraActor) encoderDiff(raw []byte) ([]byte, []string, error) {
	var cambios []string
	if quiero, ok := smartCodecWanted(a.want.SmartCodec); ok {
		nuevo, cambio, err := isapi.SetSmartCodec(raw, quiero)
		if err != nil {
			return nil, nil, fmt.Errorf("armando SmartCodec: %w", err)
		}
		if cambio {
			raw = nuevo
			cambios = append(cambios, fmt.Sprintf("SmartCodec -> %v", quiero))
		}
	}
	// ISAPI guarda los fps en centi-fps: 2000 son 20 fps. Confundirlo lleva a creer que la
	// cámara graba a 2000 fps y a escribir un valor absurdo.
	if a.want.FrameRate > 0 {
		nuevo, cambio, err := isapi.SetVideoField(raw, "maxFrameRate",
			fmt.Sprintf("%d", a.want.FrameRate*100))
		if err != nil {
			return nil, nil, fmt.Errorf("armando maxFrameRate: %w", err)
		}
		if cambio {
			raw = nuevo
			cambios = append(cambios, fmt.Sprintf("maxFrameRate -> %d fps", a.want.FrameRate))
		}
	}
	if a.want.GopFrames > 0 {
		nuevo, cambio, err := isapi.SetVideoField(raw, "GovLength",
			fmt.Sprintf("%d", a.want.GopFrames))
		if err != nil {
			return nil, nil, fmt.Errorf("armando GovLength: %w", err)
		}
		if cambio {
			raw = nuevo
			cambios = append(cambios, fmt.Sprintf("GovLength -> %d cuadros", a.want.GopFrames))
		}
	}
	return raw, cambios, nil
}

// smartCodecWanted traduce el flag a un booleano. El segundo valor es falso cuando no hay
// nada pedido, que es lo que deja la cámara como esté.
func smartCodecWanted(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "1", "enabled":
		return true, true
	case "off", "false", "0", "disabled":
		return false, true
	}
	return false, false
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

func describeTimeFix(modo, zona bool, tm *isapi.Time, want CameraConfig) string {
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
	if res.encoderErr != nil {
		a.warnLog.Printf("cámara de la puerta %d (%s), perfil de codificación: %s",
			res.door, res.host, res.encoderErr)
	}
	if res.codecChecked {
		a.buildLog.Printf("cámara de la puerta %d: codec %s, %.0f fps, SmartCodec %v",
			res.door, res.codecType, res.codecFPS, res.smartCodec)
		// El codec se reporta pero no se cambia: video/extract.go solo sabe H.264, así que
		// una cámara en H.265 va a fallar la extracción por más que todo lo demás esté bien.
		if len(res.codecType) > 0 && !strings.EqualFold(res.codecType, "H.264") {
			a.warnLog.Printf("cámara de la puerta %d (%s) codifica en %q; la extracción de "+
				"video solo maneja H.264 y va a fallar. Cambialo en la cámara",
				res.door, res.host, res.codecType)
		}
	}
	// Lo que se midió pero todavía no se escribió. Se avisa una vez por cámara para no
	// repetirlo en cada ciclo.
	if len(res.encoderPending) > 0 && !st.rebootWarned {
		st.rebootWarned = true
		a.infoLog.Printf("cámara de la puerta %d (%s): %s pendiente(s), se aplican cuando el "+
			"binario lleve %v encendido", res.door, res.host,
			strings.Join(res.encoderPending, ", "), encoderMinUptime)
	}
	if len(res.encoderPending) == 0 {
		st.rebootWarned = false
	}

	// Un valor que se escribió con OK y no quedó guardado. Se deja de intentar en esta
	// cámara: reintentarlo es un PUT por ciclo para siempre, y lo que hay que corregir es
	// el flag, no la cámara.
	if len(res.encoderStuck) > 0 {
		st.encoderGiveUp = true
		a.errLog.Printf("cámara de la puerta %d (%s): %s se escribió con OK pero NO quedó "+
			"guardado (la cámara reporta %s). Seguramente el valor pedido no está entre los "+
			"que admite: revisá /ISAPI/Streaming/channels/101/capabilities. No se vuelve a "+
			"intentar hasta el próximo arranque del binario",
			res.door, res.host, strings.Join(res.encoderStuck, ", "), res.stuckObserved)
		a.publish(ctx, res, st, "encoder_rejected")
	}

	// El reinicio se decide en el mismo ciclo que escribió, no después: acá ya se sabe que
	// el ciclo cayó dentro de la ventana, porque si no, no se habría escrito.
	if res.rebootRequired {
		a.considerReboot(ctx, res, st)
	}

	// El almacenamiento se reporta antes que la deriva: sin medio de grabación no hay
	// video para ningún evento, y es un problema más grave que un reloj corrido.
	if res.storageChecked {
		if !res.storageOK && !st.storageAlerted {
			st.storageAlerted = true
			a.errLog.Printf("cámara de la puerta %d (%s): almacenamiento en %q, no está grabando. "+
				"Ningún clip se va a poder extraer hasta que se formatee (no lo hace este binario)",
				res.door, res.host, res.storageStatus)
			a.publish(ctx, res, st, "storage")
		} else if res.storageOK && st.storageAlerted {
			st.storageAlerted = false
			a.infoLog.Printf("cámara de la puerta %d (%s): almacenamiento recuperado (%q, %d MB libres)",
				res.door, res.host, res.storageStatus, res.storageFreeMB)
			a.publish(ctx, res, st, "storage_ok")
		}
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

	// El "fixed" se omite si ya salió un "reboot": los dos llevarían el mismo arreglo en
	// `fixed` y la plataforma vería dos mensajes para un solo cambio. El "reboot" es el más
	// informativo de los dos, porque además dice que la cámara se va a caer un momento.
	if len(res.fixed) > 0 && !res.rebootRequired && len(res.encoderStuck) == 0 {
		a.publish(ctx, res, st, "fixed")
	}
}

// considerReboot reinicia la cámara que acaba de responder statusCode 7.
//
// Solo se llega acá desde un ciclo que ya escribió y recibió statusCode 7, así que no se
// vuelve a comprobar ninguna precondición: hacerlo podría dejar el cambio ya escrito pero sin
// reiniciar, que es el peor estado —guardado e inerte, con el API reportando el valor nuevo.
//
// Corre en el hilo del actor, que es donde vive el estado, y por eso puede marcar `rebooted`
// antes de lanzar la petición: el candado se cierra sin carrera. El PUT en sí va en una
// goroutine porque tarda y Receive no puede bloquearse.
func (a *CameraActor) considerReboot(ctx actor.Context, res *msgCameraResult, st *camTimeState) {
	if st.rebooted {
		a.errLog.Printf("cámara de la puerta %d (%s) volvió a pedir reinicio DESPUÉS de haberse "+
			"reiniciado: el cambio no quedó guardado. No se reinicia otra vez; revisá si el "+
			"modelo acepta ese ajuste", res.door, res.host)
		return
	}

	st.rebooted = true
	st.rebootWarned = false
	a.warnLog.Printf("reiniciando la cámara de la puerta %d (%s) para aplicar el perfil de "+
		"codificación; va a dejar de grabar y de contar durante el arranque",
		res.door, res.host)
	a.publish(ctx, res, st, "reboot")

	self, system := ctx.Self(), ctx.ActorSystem()
	door, host := res.door, res.host
	user, pass := a.user, a.pass
	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), cameraHTTPTimeout)
		defer cancel()
		err := isapi.New(host, user, pass, cameraHTTPTimeout).Reboot(cctx)
		system.Root.Send(self, &msgRebootDone{door: door, host: host, err: err})
	}()
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
		// Storage solo va cuando se pudo consultar el medio de grabación.
		Storage       string `json:"storage,omitempty"`
		StorageFreeMB int    `json:"storage_free_mb,omitempty"`
		// Codec describe el perfil observado. Va en el evento para que la plataforma pueda
		// detectar de lejos una cámara en H.265 o con SmartCodec activo, que son las dos
		// condiciones que dejan un clip inservible aunque la extracción "funcione".
		VideoCodec string  `json:"video_codec,omitempty"`
		VideoFPS   float64 `json:"video_fps,omitempty"`
		SmartCodec *bool   `json:"smart_codec,omitempty"`
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
	if res.storageChecked {
		val.Storage = res.storageStatus
		val.StorageFreeMB = res.storageFreeMB
	}
	if res.codecChecked {
		val.VideoCodec = res.codecType
		val.VideoFPS = res.codecFPS
		v := res.smartCodec
		val.SmartCodec = &v
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
