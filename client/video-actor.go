package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dumacp/pubsub"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/go-hikvision/isapi"
	"github.com/dumacp/go-hikvision/video"
)

const (
	// videoTrack es el track de grabación del canal 1.
	videoTrack = 101
	// videoSettleDelay es el margen que se espera después del final de la ventana
	// antes de pedirla. El recorte incluye video POSTERIOR al evento, así que al
	// recibirlo esa parte todavía no está grabada: pedirla de inmediato hace que la
	// verificación de cobertura falle porque la ventana termina en el futuro.
	videoSettleDelay = 4 * time.Second
	// videoMinPostRoll es el video que debe quedar DESPUÉS de un evento para que su
	// cruce entre completo en el clip. Es lo que limita cuántos eventos seguidos
	// pueden compartir una misma ventana.
	videoMinPostRoll = 2 * time.Second
	// videoMinFree es el espacio libre mínimo bajo el cual se deja de extraer. El
	// binario no borra clips —de eso se encarga quien los suba— pero tampoco puede
	// llenar el disco, porque en la misma partición vive la boltdb del conteo.
	videoMinFree = 512 << 20
)

// VideoActor extrae de la cámara el video de cada paso y lo deja en disco.
//
// La reproducción de la cámara va a tiempo real: un recorte de diez segundos tarda
// diez segundos. Por eso se extrae de a uno, con una cola acotada, y el trabajo pesado
// ocurre en una goroutine y nunca dentro de Receive.
type VideoActor struct {
	*Logger
	cameras  []string
	user     string
	pass     string
	dir      string
	preRoll  time.Duration
	duration time.Duration
	maxQueue int

	pending  []videoJob
	working  bool
	tickPend bool

	// camInfo cachea la identidad de cada cámara por puerta. No cambia, así que se
	// consulta una sola vez y no en cada extracción.
	camInfo map[int32]camIdent

	// hostname del equipo donde corre el binario, resuelto una vez al construir el
	// actor: identifica de qué gateway salió el clip cuando todos suben al mismo lugar.
	hostname string
	cancel   func()
	seq      int
}

// camIdent identifica físicamente la cámara que grabó el clip, para que el consumidor
// del directorio sepa de qué equipo salió sin cruzar con otra fuente.
type camIdent struct {
	serial string
	mac    string
}

type videoJob struct {
	door    int32
	tipo    messages.Event_EventType
	when    time.Time
	counter int64
	uid     string
}

// msgVideoDone avisa que terminó una extracción; lo manda la goroutine al actor.
type msgVideoDone struct {
	// group son todos los eventos que comparten este clip: el primero define la
	// ventana y los demás caen dentro de ella.
	group []videoJob
	err   error
	res   *video.Result
	// dest es la ruta final del clip, vacía si falló.
	dest string
	// sinCobertura distingue "la cámara no tiene ese tramo" de un error real.
	sinCobertura bool
	// ident es la identidad de la cámara si hubo que consultarla en esta pasada; el
	// actor la cachea al recibirla.
	ident camIdent
	// metas son las rutas de los sidecars escritos, en el mismo orden que group.
	metas []string
}

// msgVideoTick despierta al actor cuando la ventana del primer pendiente ya terminó de
// grabarse. Sirve para agrupar con la cola lo más llena posible.
type msgVideoTick struct{}

// MsgClockStep avisa que el reloj de una cámara saltó. Los trabajos pendientes de esa
// puerta quedan inservibles: su marca de tiempo ya no apunta a la misma posición de la
// grabación, y extraerlos daría video de otro momento con el nombre del evento.
type MsgClockStep struct {
	ID int32
}

// NewVideoActor crea el actor.
func NewVideoActor(cameras []string, user, pass, dir string, preRoll, duration time.Duration, maxQueue int) *VideoActor {
	if maxQueue <= 0 {
		maxQueue = 500
	}
	a := &VideoActor{
		cameras:  cameras,
		user:     user,
		pass:     pass,
		dir:      dir,
		preRoll:  preRoll,
		duration: duration,
		maxQueue: maxQueue,
	}
	a.camInfo = make(map[int32]camIdent)
	// Un error acá no es motivo para deshabilitar nada: el campo queda vacío.
	a.hostname, _ = os.Hostname()
	a.Logger = &Logger{}
	return a
}

// Receive func Receive in actor
func (a *VideoActor) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		a.initLogs()
		a.infoLog.Printf("actor started \"%s\", dir=%q preRoll=%v duracion=%v cola=%d",
			ctx.Self().Id, a.dir, a.preRoll, a.duration, a.maxQueue)

	case *actor.Stopping:
		a.warnLog.Println("stopped actor")
		if a.cancel != nil {
			a.cancel()
		}

	case *messages.Event:
		a.enqueue(ctx, msg)

	case *MsgClockStep:
		descartados := 0
		keep := a.pending[:0]
		for _, j := range a.pending {
			if j.door == msg.ID {
				descartados++
				continue
			}
			keep = append(keep, j)
		}
		a.pending = keep
		if descartados > 0 {
			a.warnLog.Printf("reloj de la puerta %d saltó: %d recortes pendientes descartados, "+
				"su marca de tiempo ya no apunta al mismo tramo grabado", msg.ID, descartados)
		}

	case *msgVideoTick:
		a.tickPend = false
		a.next(ctx)

	case *msgVideoDone:
		a.working = false
		anchor := msg.group[0]
		if len(msg.ident.serial) > 0 {
			a.camInfo[anchor.door] = msg.ident
		}
		switch {
		case msg.sinCobertura:
			a.warnLog.Printf("sin grabación para %s en la cámara de la puerta %d, %d evento(s) sin video",
				anchor.when.Format(time.RFC3339), anchor.door, len(msg.group))
		case msg.err != nil:
			a.errLog.Printf("extrayendo video de la puerta %d (%s, %d evento(s)): %s",
				anchor.door, anchor.when.Format(time.RFC3339), len(msg.group), msg.err)
		default:
			a.infoLog.Printf("video %s: %d muestras, %v, %d bytes en %v, %d evento(s) en la ventana",
				filepath.Base(msg.dest), msg.res.Samples, msg.res.Duration.Round(time.Millisecond),
				msg.res.Bytes, msg.res.Elapsed.Round(time.Millisecond), len(msg.group))
			a.publishVideoReady(ctx, msg)
		}
		a.next(ctx)
	}
}

// enqueue agrega el paso a la cola si hay cámara y espacio.
func (a *VideoActor) enqueue(ctx actor.Context, msg *messages.Event) {
	switch msg.GetType() {
	case messages.Event_INPUT, messages.Event_OUTPUT:
	default:
		return
	}
	if msg.GetTimestamp() <= 0 {
		// Un evento sin marca de tiempo no se puede ubicar en la grabación. Pasa con
		// los eventos replicados de una boltdb anterior a este campo.
		a.buildLog.Printf("evento sin timestamp, no se extrae video: %v", msg)
		return
	}
	if a.cameraFor(msg.GetID()) == "" {
		a.warnLog.Printf("sin -camera para la puerta %d, no se puede extraer video", msg.GetID())
		return
	}
	if len(a.pending) >= a.maxQueue {
		a.warnLog.Printf("cola de video llena (%d), se descarta el paso de la puerta %d en %s",
			a.maxQueue, msg.GetID(), time.Unix(msg.GetTimestamp(), 0).Format(time.RFC3339))
		return
	}
	a.pending = append(a.pending, videoJob{
		door:    msg.GetID(),
		tipo:    msg.GetType(),
		when:    time.Unix(msg.GetTimestamp(), 0),
		counter: msg.GetValue(),
		uid:     msg.GetUid(),
	})
	a.next(ctx)
}

// next arranca la siguiente extracción si no hay ninguna en curso.
//
// Agrupa: el primer pendiente define la ventana y se absorben todos los demás de la
// misma puerta cuyo evento caiga dentro con al menos videoMinPostRoll de video después.
// Así una ráfaga de pasos comparte un clip en vez de bajar la misma ventana N veces —
// medido: la cámara ajusta el starttime al keyframe anterior, con lo que dos eventos del
// mismo GOP devuelven bytes idénticos.
func (a *VideoActor) next(ctx actor.Context) {
	if a.working || len(a.pending) == 0 {
		return
	}

	anchor := a.pending[0]
	desde := anchor.when.Add(-a.preRoll)
	hasta := desde.Add(a.duration)

	// Esperar aquí, ANTES de agrupar, a que la cámara haya grabado toda la ventana.
	// Además de ser necesario para que exista el video, deja que la cola se llene:
	// agrupar al recibir el primer evento daría un grupo de uno solo, porque los pasos
	// siguientes todavía no llegaron, y volverían los clips duplicados.
	if espera := time.Until(hasta.Add(videoSettleDelay)); espera > 0 {
		if !a.tickPend {
			a.tickPend = true
			self, system := ctx.Self(), ctx.ActorSystem()
			time.AfterFunc(espera, func() { system.Root.Send(self, &msgVideoTick{}) })
		}
		return
	}

	// Último instante que todavía deja videoMinPostRoll de cola dentro del clip.
	limite := hasta.Add(-videoMinPostRoll)

	group := []videoJob{anchor}
	resto := a.pending[:0:0]
	for _, j := range a.pending[1:] {
		if j.door == anchor.door && !j.when.Before(desde) && !j.when.After(limite) {
			group = append(group, j)
			continue
		}
		resto = append(resto, j)
	}
	a.pending = resto
	a.working = true

	self := ctx.Self()
	system := ctx.ActorSystem()
	host := a.cameraFor(anchor.door)
	ident := a.camInfo[anchor.door]
	hostname := a.hostname

	dir := filepath.Join(a.dir, anchor.when.Local().Format("2006-01-02"))
	// El clip no lleva sufijo in/out: es de una puerta y una ventana de tiempo, y puede
	// contener entradas y salidas. El tipo pertenece a cada evento, o sea al sidecar.
	// El nombre es autosuficiente —fecha, hora, equipo, puerta y uid— para poder
	// buscarlo en un repositorio final donde se pierda la estructura de directorios.
	clip := uniquePath(dir, fmt.Sprintf("%s_%s_p%d_%s",
		anchor.when.Local().Format("20060102T150405"), hostname, anchor.door, anchor.uid), ".mp4")

	plazo := a.duration*3 + videoSettleDelay + 30*time.Second
	cctx, cancel := context.WithTimeout(context.Background(), plazo)
	a.cancel = cancel

	go func() {
		defer cancel()
		done := &msgVideoDone{group: group, dest: clip}

		if libre, err := freeSpace(a.dir); err == nil && libre < videoMinFree {
			done.err = fmt.Errorf("espacio libre insuficiente en %s: %d MB", a.dir, libre>>20)
			system.Root.Send(self, done)
			return
		}

		// Guarda imprescindible: pedir un instante sin grabación no da error, devuelve
		// el tramo disponible más cercano, y el clip quedaría con el nombre de estos
		// eventos mostrando otro momento.
		cli := isapi.New(host, a.user, a.pass, 15*time.Second)

		// Identidad de la cámara: una sola vez por puerta. Si falla no se aborta la
		// extracción — el clip vale más que su metadato.
		if len(ident.serial) == 0 {
			if info, err := cli.GetDeviceInfo(cctx); err == nil {
				ident = camIdent{serial: info.SerialNumber, mac: info.MACAddress}
				done.ident = ident
			}
		}

		rec, err := cli.HasCoverage(cctx, videoTrack, desde, hasta,
			fmt.Sprintf("C0DE0001-0000-0000-0000-%012d", anchor.when.Unix()%1e12))
		if err != nil {
			done.err = fmt.Errorf("consultando grabaciones: %w", err)
			system.Root.Send(self, done)
			return
		}
		if rec == nil {
			done.sinCobertura = true
			system.Root.Send(self, done)
			return
		}

		res, err := video.Extract(cctx, video.Request{
			Host: host, User: a.user, Pass: a.pass, Track: videoTrack,
			Start: desde, Duration: a.duration, Dest: clip,
		})
		if err != nil {
			done.err = err
			system.Root.Send(self, done)
			return
		}
		done.res = res

		// Un sidecar por evento, todos apuntando al mismo clip y diciendo en qué
		// segundo de ese clip ocurrió su cruce.
		for _, j := range group {
			_ = j
			meta := uniquePath(dir, fmt.Sprintf("%s_%s_p%d_%s_%s",
				j.when.Local().Format("20060102T150405"), hostname, j.door,
				tipoTexto(j.tipo), j.uid), ".json")
			if err := writeSidecar(meta, j, hostname, host, ident, clip, desde, a.duration, res); err != nil {
				// El clip ya está en disco; el sidecar es complementario.
				done.err = fmt.Errorf("clip escrito pero falló el sidecar: %w", err)
				continue
			}
			done.metas = append(done.metas, meta)
		}
		system.Root.Send(self, done)
	}()
}

// publishVideoReady avisa por MQTT que el clip de estos eventos ya está en disco.
//
// Se publica acá y no al contar el paso a propósito: en ese momento no se sabe el nombre
// final del archivo ni si la extracción va a tener éxito, así que anunciarlo antes
// dejaría a la plataforma esperando un archivo que puede no existir nunca. El evento
// COUNTERSDOOR solo lleva el event_id, y este mensaje lo completa cuando es un hecho.
//
// Va uno por evento, aunque varios compartan el clip, para que el cruce del otro lado
// sea 1 a 1 por event_id y no haya que desarmar listas.
func (a *VideoActor) publishVideoReady(ctx actor.Context, done *msgVideoDone) {
	if ctx.Parent() == nil {
		return
	}
	ident := a.camInfo[done.group[0].door]
	for i, j := range done.group {
		if i >= len(done.metas) {
			break
		}
		val := struct {
			ID              int32  `json:"id"`
			Type            string `json:"type"`
			EventID         string `json:"event_id"`
			GatewayHostname string `json:"gateway_hostname"`
			Metadata        string `json:"metadata"`
			Clip            string `json:"clip"`
			CameraSerial    string `json:"camera_serial,omitempty"`
		}{
			ID:              j.door,
			Type:            "CAMERA",
			EventID:         j.uid,
			GatewayHostname: a.hostname,
			Metadata:        filepath.Base(done.metas[i]),
			Clip:            filepath.Base(done.dest),
			CameraSerial:    ident.serial,
		}
		data, err := json.Marshal(&pubsub.Message{
			Timestamp: float64(time.Now().UnixNano()) / 1000000000,
			Type:      "COUNTERSDOORVIDEO",
			Value:     val,
		})
		if err != nil {
			a.errLog.Printf("armando COUNTERSDOORVIDEO: %s", err)
			continue
		}
		a.buildLog.Printf("%s", data)
		ctx.Send(ctx.Parent(), &msgAddEvent{data: data})
	}
}

// uniquePath devuelve dir/base+ext, agregando un sufijo si ya existe.
func uniquePath(dir, base, ext string) string {
	name := base
	for i := 2; i < 100; i++ {
		if _, err := os.Stat(filepath.Join(dir, name+ext)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s_%d", base, i)
	}
	return filepath.Join(dir, name+ext)
}

func tipoTexto(t messages.Event_EventType) string {
	if t == messages.Event_OUTPUT {
		return "out"
	}
	return "in"
}

func (a *VideoActor) cameraFor(door int32) string {
	if int(door) < len(a.cameras) {
		return a.cameras[door]
	}
	return ""
}

// sidecar describe un evento y el clip que lo contiene, para quien después lo suba,
// sin que tenga que parsear el nombre del archivo ni consultar nada.
//
// Varios eventos pueden compartir un mismo Clip: OffsetS dice en qué segundo de ese
// video ocurrió este cruce, así que no hace falta buscarlo.
//
// ClipStart es el instante PEDIDO. El video real puede empezar hasta un GOP antes
// (medido: 2 s a 20 fps) porque la cámara posiciona la reproducción en el keyframe
// anterior. Eso da más pre-roll que el configurado, nunca menos, pero conviene no
// tomar este valor como el primer frame exacto, ni OffsetS como exacto al frame.
type sidecar struct {
	// EventID es la misma llave que viaja en el evento COUNTERSDOOR de MQTT.
	EventID string `json:"event_id"`
	// GatewayHostname es el equipo donde corrió el binario que extrajo el clip.
	GatewayHostname string `json:"gateway_hostname"`
	Door            int32  `json:"door"`
	Type            string `json:"type"`
	EventTime       string `json:"event_time"`
	// EventTimeMs es el mismo instante en milisegundos Unix UTC. La resolución de
	// origen es de un segundo: la cámara envía el dateTime sin fracción, así que estos
	// milisegundos siempre terminan en 000.
	EventTimeMs int64   `json:"event_time_ms"`
	Clip        string  `json:"clip"`
	ClipStart   string  `json:"clip_start_requested"`
	OffsetS     float64 `json:"offset_s"`
	DurationS   int     `json:"duration_s"`
	Camera      string  `json:"camera"`
	// CameraSerial y CameraMAC identifican el equipo que grabó el clip, para que el
	// consumidor no dependa de la IP, que cambia con el direccionamiento.
	CameraSerial string `json:"camera_serial"`
	CameraMAC    string `json:"camera_mac"`
	CamCounter   int64  `json:"camera_counter"`
	Samples      int    `json:"samples"`
	Bytes        int64  `json:"bytes"`
}

func writeSidecar(path string, job videoJob, gateway, host string, ident camIdent, clip string, start time.Time, dur time.Duration, res *video.Result) error {
	s := sidecar{
		EventID:         job.uid,
		GatewayHostname: gateway,
		Door:            job.door,
		Type:            tipoTexto(job.tipo),
		EventTime:       job.when.Local().Format(time.RFC3339),
		EventTimeMs:     job.when.UTC().UnixMilli(),
		Clip:            filepath.Base(clip),
		ClipStart:       start.Local().Format(time.RFC3339),
		OffsetS:         job.when.Sub(start).Seconds(),
		DurationS:       int(dur.Seconds()),
		Camera:          host,
		CameraSerial:    ident.serial,
		CameraMAC:       ident.mac,
		CamCounter:      job.counter,
		Samples:         res.Samples,
		Bytes:           res.Bytes,
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Mismo criterio que el clip: escribir y renombrar, para que quien recorra el
	// directorio no lea un JSON a medio escribir.
	tmp := path + ".part"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// freeSpace devuelve los bytes libres de la partición que contiene dir.
func freeSpace(dir string) (uint64, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
