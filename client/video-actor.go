package client

import (
	"context"
	"encoding/json"
	"errors"
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
	// videoIndiceEspera es cuánto se sigue esperando, desde el primer intento, a que el
	// índice de grabaciones de la cámara alcance el final de la ventana. Medido en la G1
	// (V4.2.2): el índice va hasta ~2 min atrasado y avanza de a saltos, así que a los
	// videoSettleDelay la ventana casi nunca figura grabada aunque ya lo esté. Pasado
	// este plazo se da por "sin grabación".
	videoIndiceEspera = 3 * time.Minute
	// videoIndiceReintento es cada cuánto se vuelve a preguntar mientras tanto.
	videoIndiceReintento = 20 * time.Second
	// videoMinPostRoll es el video que debe quedar DESPUÉS de un evento para que su
	// cruce entre completo en el clip. Es lo que limita cuántos eventos seguidos
	// pueden compartir una misma ventana.
	videoMinPostRoll = 2 * time.Second
	// videoClockSlack es el margen que se le da a la espera de asentamiento antes de
	// declarar que los relojes no coinciden.
	videoClockSlack = 30 * time.Second
	// videoMaxGapWarn es el hueco entre fotos a partir del cual se avisa que el clip se va a
	// ver congelado. A 20 fps lo normal es 0.05 s, y el caso malo medido con H.264+ fue de
	// 5.7 s, así que un segundo queda holgado respecto del jitter normal y muy por debajo del
	// problema real.
	videoMaxGapWarn = 1 * time.Second
)

// VideoMinFreeDefault es el espacio libre mínimo por defecto bajo el cual se deja de
// extraer, configurable con -videoMinFree.
//
// El binario no borra clips —de eso se encarga quien los suba— pero tampoco puede llenar
// el disco, porque en la misma partición vive la boltdb del conteo: quedarse sin video de
// un pasajero cuesta un clip, quedarse sin disco cuesta el acumulado del vehículo.
//
// Tampoco sobrescribe ni rota: los nombres llevan el uid del evento, así que no hay un
// "más viejo" al que apuntar, y borrar el clip más antiguo sería borrar justamente el que
// lleva más tiempo esperando que lo suban. Este binario no sabe cuáles ya se subieron
// —eso lo sabe quien los sube—, así que ante el disco lleno prefiere perder el video que
// todavía no existe antes que uno ya grabado.
const VideoMinFreeDefault = 512 << 20

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
	// minFree es el piso de espacio libre; 0 deshabilita la comprobación.
	minFree uint64

	pending  []videoJob
	working  bool
	tickPend bool
	// camaraEncendida es falso mientras go-ignition tenga el vehículo apagado y
	// CountingActor le haya cortado la energía a la cámara. Sin -cameraPowerGPIO nadie
	// manda MsgCameraPower nunca y queda siempre en true.
	camaraEncendida bool
	// clockWarned evita repetir el aviso de relojes desfasados en cada evento.
	clockWarned bool
	// sinEspacioAvisado evita repetir el aviso del disco lleno en cada evento. Con el
	// disco al tope fallan TODOS los pasos, así que un mensaje por evento serían cientos
	// por día diciendo lo mismo, y el aviso se perdería entre sus propias repeticiones.
	// Se rearma cuando una extracción vuelve a encontrar espacio.
	sinEspacioAvisado bool
	// medirWarned evita repetir el aviso de "no se pudo medir el disco". Ese caso sigue
	// adelante a propósito —no poder medir no es razón para dejar al vehículo sin video—
	// pero antes lo hacía en silencio, y un fail-open silencioso no se puede auditar.
	medirWarned bool
	// lastEvent recuerda la hora del último evento de cada puerta, para saber si los
	// pasajeros que cuenta un evento caben en la ventana del clip.
	lastEvent map[int32]time.Time

	// camInfo cachea la identidad de cada cámara por puerta. No cambia, así que se
	// consulta una sola vez y no en cada extracción.
	camInfo map[int32]camIdent

	// horaLocal recuerda, por puerta, si la cámara lee los instantes de búsqueda y de
	// reproducción como su reloj de pared en vez de UTC (la G1 con V4.2.2). Se aprende de
	// la primera búsqueda que encuentra grabación y ahorra el intento fallido en cada
	// pasajero. Ausente es "no se sabe", y se empieza por UTC, la convención de campo.
	// No se invalida en caliente: un cambio de cámaras reconstruye este actor.
	horaLocal map[int32]bool

	// burstGap y maxDuration encadenan una ráfaga en un solo clip (ver armarRafaga). Con
	// burstGap en cero se vuelve a la ventana fija de siempre.
	burstGap    time.Duration
	maxDuration time.Duration

	// sinDescarga marca las puertas cuya cámara no ofrece /ISAPI/ContentMgmt/download
	// (la G0 responde methodNotAllowed): ahí se extrae por RTSP sin volver a intentar la
	// descarga. Es propiedad del modelo, así que se aprende una vez por puerta.
	sinDescarga map[int32]bool

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
	// primerIntento es cuándo se buscó por primera vez la grabación de este evento, con
	// el reloj del GATEWAY (no el de la cámara, que puede estar corrido): es lo que
	// acota videoIndiceEspera. Cero si todavía no se intentó.
	primerIntento time.Time
	// noAntes posterga el próximo intento cuando el índice de la cámara iba atrasado,
	// también con el reloj del gateway.
	noAntes time.Time
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
	// sinEspacio distingue el disco lleno de un error de extracción: no es una falla de
	// la cámara ni del recorte, y se avisa una sola vez en vez de por evento.
	sinEspacio bool
	// libre son los bytes libres medidos, válidos solo si espacioMedido.
	libre         uint64
	espacioMedido bool
	// medirErr se llena cuando no se pudo medir el disco. La extracción siguió igual.
	medirErr error
	// ident es la identidad de la cámara si hubo que consultarla en esta pasada; el
	// actor la cachea al recibirla.
	ident camIdent
	// metas son las rutas de los sidecars escritos, en el mismo orden que group.
	metas []string
	// horaLocal es la convención de hora con la que la cámara respondió, válida solo si
	// horaConocida: la búsqueda o el recorte lo confirmaron en esta pasada.
	horaLocal    bool
	horaConocida bool
	// sinDescarga indica que la cámara respondió que no ofrece la descarga HTTP.
	sinDescarga bool
	// reintentar indica que el índice de la cámara todavía no llegaba al final de la
	// ventana y queda plazo para esperarlo: el grupo vuelve a la cola.
	reintentar bool
	// indexadoHasta es hasta dónde llegaba ese índice, para el log.
	indexadoHasta time.Time
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
func NewVideoActor(cameras []string, user, pass, dir string, preRoll, duration time.Duration,
	maxQueue int, minFree uint64) *VideoActor {
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
		minFree:  minFree,
	}
	a.camInfo = make(map[int32]camIdent)
	a.horaLocal = make(map[int32]bool)
	a.sinDescarga = make(map[int32]bool)
	a.lastEvent = make(map[int32]time.Time)
	// Sin información de ignition se asume encendida; ver el campo.
	a.camaraEncendida = true
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
		a.infoLog.Printf("actor started \"%s\", dir=%q preRoll=%v duracion=%v cola=%d piso=%s",
			ctx.Self().Id, a.dir, a.preRoll, a.duration, a.maxQueue, describeMinFree(a.minFree))

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

	case *MsgCameraPower:
		a.camaraEncendida = msg.On
		if msg.On {
			a.infoLog.Println("la camara volvio a tener alimentacion, se reanuda la extraccion")
			a.next(ctx)
			break
		}
		// Los pendientes se descartan por la misma razón que con MsgClockStep: ya no
		// se pueden cumplir. Ahí la marca de tiempo dejaba de apuntar al tramo
		// correcto; acá el tramo directamente no va a existir, porque la cámara deja de
		// grabar al quedarse sin energía. Guardarlos solo serviría para que al volver
		// se intenten todos contra una grabación que no está.
		if n := len(a.pending); n > 0 {
			a.warnLog.Printf("vehiculo apagado: %d recortes pendientes descartados, "+
				"la camara no va a grabar esa ventana", n)
			a.pending = a.pending[:0]
		}
		a.infoLog.Println("vehiculo apagado: no se extrae mas video hasta que vuelva la alimentacion")

	case *msgVideoTick:
		a.tickPend = false
		a.next(ctx)

	case *msgVideoDone:
		a.working = false
		anchor := msg.group[0]
		if len(msg.ident.serial) > 0 {
			a.camInfo[anchor.door] = msg.ident
		}
		if msg.sinDescarga && !a.sinDescarga[anchor.door] {
			a.sinDescarga[anchor.door] = true
			a.infoLog.Printf("la cámara de la puerta %d no ofrece descarga de grabaciones: "+
				"su video se extrae por RTSP", anchor.door)
		}
		if msg.horaConocida {
			if previa, ok := a.horaLocal[anchor.door]; !ok || previa != msg.horaLocal {
				a.infoLog.Printf("la cámara de la puerta %d indexa sus grabaciones en %s",
					anchor.door, map[bool]string{true: "hora local (reloj de pared)", false: "UTC"}[msg.horaLocal])
			}
			a.horaLocal[anchor.door] = msg.horaLocal
		}
		// El disco se informa antes de mirar el resultado de la extracción, porque
		// condiciona todo lo demás y su aviso no depende de cómo salió este clip.
		a.reportarEspacio(msg)

		switch {
		case msg.sinEspacio:
			// Ya avisado en reportarEspacio; acá solo queda el detalle para -debug. Con el
			// disco al tope falla TODA extracción, así que un ERROR por evento sería un
			// mensaje por pasajero repitiendo lo mismo hasta llenar el log.
			a.buildLog.Printf("sin espacio para el video de la puerta %d (%s, %d evento(s))",
				anchor.door, anchor.when.Format(time.RFC3339), len(msg.group))
		case msg.reintentar:
			// No se pierde nada: el grupo vuelve al frente de la cola y next lo reagrupa,
			// así que los eventos que lleguen mientras tanto pueden sumarse al mismo clip.
			a.buildLog.Printf("la cámara de la puerta %d tiene indexado hasta %s y la ventana "+
				"de %s termina después: se reintenta en %v (%d evento(s))",
				anchor.door, msg.indexadoHasta.Format("15:04:05"), anchor.when.Format("15:04:05"),
				videoIndiceReintento, len(msg.group))
			noAntes := time.Now().Add(videoIndiceReintento)
			for i := range msg.group {
				msg.group[i].noAntes = noAntes
			}
			a.pending = append(append(make([]videoJob, 0, len(msg.group)+len(a.pending)),
				msg.group...), a.pending...)
		case msg.sinCobertura && !msg.indexadoHasta.IsZero():
			a.warnLog.Printf("sin grabación para %s en la cámara de la puerta %d, %d evento(s) "+
				"sin video: tras %v el índice de la cámara sigue llegando solo hasta %s",
				anchor.when.Format(time.RFC3339), anchor.door, len(msg.group),
				videoIndiceEspera, msg.indexadoHasta.Format("15:04:05"))
		case msg.sinCobertura:
			a.warnLog.Printf("sin grabación para %s en la cámara de la puerta %d, %d evento(s) sin video",
				anchor.when.Format(time.RFC3339), anchor.door, len(msg.group))
		case msg.err != nil:
			a.errLog.Printf("extrayendo video de la puerta %d (%s, %d evento(s)): %s",
				anchor.door, anchor.when.Format(time.RFC3339), len(msg.group), msg.err)
		default:
			a.infoLog.Printf("video %s: %s por %s, %d muestras, %v, hueco máx %v, %d bytes en %v, %d evento(s) en la ventana",
				filepath.Base(msg.dest), msg.res.Codec, msg.res.Via, msg.res.Samples,
				msg.res.Duration.Round(time.Millisecond),
				msg.res.MaxGap.Round(time.Millisecond), msg.res.Bytes,
				msg.res.Elapsed.Round(time.Millisecond), len(msg.group))
			// Un hueco grande significa que la cámara dejó de emitir fotos, y el clip se ve
			// congelado por más que la extracción haya salido bien. La causa medida es
			// H.264+ (SmartCodec) con escena quieta: 5.7 s sin una sola foto nueva. Se avisa
			// acá porque sin este log se culparía al extractor.
			if msg.res.MaxGap > videoMaxGapWarn {
				a.warnLog.Printf("video %s: hueco de %v sin fotos (normal a 20 fps: 0.05s). "+
					"El clip se va a ver congelado en ese tramo. Causa medida: H.264+ activo "+
					"en la cámara; se apaga con -smartCodec off",
					filepath.Base(msg.dest), msg.res.MaxGap.Round(time.Millisecond))
			}
			a.publishVideoReady(ctx, msg)
		}
		a.next(ctx)
	}
}

// reportarEspacio deja constancia del disco una sola vez por transición, no por evento.
//
// Las dos condiciones que informa —bajo el piso, y no se pudo medir— afectan a TODOS los
// pasos por igual, así que repetirlas en cada uno llenaría el log de cientos de líneas
// idénticas por día y escondería justamente lo que hay que ver. El aviso se rearma cuando
// una extracción vuelve a encontrar espacio, para que un segundo episodio se note.
func (a *VideoActor) reportarEspacio(msg *msgVideoDone) {
	if msg.medirErr != nil {
		if !a.medirWarned {
			a.medirWarned = true
			a.warnLog.Printf("no se pudo medir el espacio libre de %s (%s): se sigue extrayendo "+
				"igual, pero el piso de %s queda sin vigilar",
				a.dir, msg.medirErr, describeMinFree(a.minFree))
		}
		return
	}
	if !msg.espacioMedido {
		return
	}
	a.medirWarned = false

	switch {
	case msg.sinEspacio:
		if !a.sinEspacioAvisado {
			a.sinEspacioAvisado = true
			a.warnLog.Printf("espacio libre en %s por debajo del piso: %d MB contra %d MB. "+
				"Se DEJA de extraer video; el conteo y el MQTT siguen normales. No se borra "+
				"ni se sobrescribe ningún clip: los que están en disco quedan para quien los "+
				"suba. Este aviso no se repite hasta que haya espacio otra vez",
				a.dir, msg.libre>>20, a.minFree>>20)
		}
	case a.sinEspacioAvisado:
		a.sinEspacioAvisado = false
		a.infoLog.Printf("espacio libre en %s recuperado: %d MB, se vuelve a extraer video",
			a.dir, msg.libre>>20)
	}
}

// describeMinFree formatea el piso para los logs, incluido el caso deshabilitado.
func describeMinFree(minFree uint64) string {
	if minFree == 0 {
		return "sin piso de disco"
	}
	return fmt.Sprintf("%d MB", minFree>>20)
}

// enqueue agrega el paso a la cola. NO comprueba el espacio en disco: eso se mide justo
// antes de cada extracción, en la goroutine, porque entre encolar y extraer pueden pasar
// minutos de cola y el disco puede haber cambiado.
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
	if !a.camaraEncendida {
		// Encolarlo no serviría: la cámara no está grabando, así que esa ventana no va
		// a existir nunca. El conteo del paso sigue su camino normal; lo único que se
		// pierde es su video.
		a.buildLog.Printf("camara sin alimentacion, no se extrae el video de la puerta %d", msg.GetID())
		return
	}
	if len(a.pending) >= a.maxQueue {
		a.warnLog.Printf("cola de video llena (%d), se descarta el paso de la puerta %d en %s",
			a.maxQueue, msg.GetID(), time.Unix(msg.GetTimestamp(), 0).Format(time.RFC3339))
		return
	}
	when := time.Unix(msg.GetTimestamp(), 0)

	// Un evento puede traer más de un pasajero, y entonces el clip no siempre los muestra
	// todos. La cámara manda acumulados, así que el incremento es la diferencia contra el
	// último visto: si el binario estuvo un rato sin recibir eventos —recién arrancado, o la
	// cámara sin alcanzarlo— ese incremento junta cruces repartidos en minutos, y el clip
	// cubre solo el último. Medido en campo: un evento con 8 pasajeros sobre un clip donde
	// se ve una persona.
	//
	// Se distinguen por el tiempo desde el evento anterior de esa puerta: si el anterior es
	// más viejo que el inicio del clip, los cruces que cuenta este evento empezaron antes de
	// lo grabado. Sin evento anterior —el primero tras arrancar— se asume lo mismo.
	//
	// El conteo publicado es correcto en los dos casos; lo que avisa esto es que el video no
	// alcanza a respaldarlo, para que nadie lea el clip como si mostrara los N pasajeros.
	if msg.GetValue() > 1 {
		previo, hubo := a.lastEvent[msg.GetID()]
		if !hubo || previo.Before(when.Add(-a.preRoll)) {
			a.warnLog.Printf("el evento de la puerta %d trae %d pasajeros pero el clip "+
				"arranca en %s: los cruces anteriores a ese instante no quedaron grabados. "+
				"El conteo es correcto, el video cubre solo el final",
				msg.GetID(), msg.GetValue(), when.Add(-a.preRoll).Format("15:04:05"))
		}
	}
	a.lastEvent[msg.GetID()] = when

	a.pending = append(a.pending, videoJob{
		door:    msg.GetID(),
		tipo:    msg.GetType(),
		when:    when,
		counter: msg.GetValue(),
		uid:     msg.GetUid(),
	})
	a.next(ctx)
}

// rafaga es un grupo de pasos de una misma puerta que van a un solo clip.
type rafaga struct {
	// indices son las posiciones en la cola de los pasos del grupo; el primero es el
	// ancla (la posición 0).
	indices []int
	// desde y hasta son la ventana del clip; ultimo, el último paso del grupo.
	desde, hasta, ultimo time.Time
}

// armarRafaga decide qué pasos de la cola van al clip que abre el primero.
//
// La ventana de un paso solo es la de siempre: preRoll antes y (duration - preRoll)
// después. Un paso de la misma puerta entra al grupo si:
//
//   - cae dentro de esa ventana con al menos videoMinPostRoll de cola, que es la regla de
//     antes y vale siempre; o
//   - llega hasta burstGap después del último paso del grupo y el clip no pasa de
//     maxDuration. Entonces la ventana se ALARGA hasta (duration - preRoll) después de él.
//
// Por qué: con pasos cada 5–6 s —una fila de gente— la ventana fija solo absorbía los que
// caían a 4 s, y salía un clip por paso repitiendo casi el mismo video. Medido el
// 2026-11-07 en FLO-W7-0138: 6 pasos en 27 s dieron 5 clips y 2.4 MB, casi todo
// repetido; encadenados son un clip de 43 s y ~1.1 MB, con un sidecar por paso.
//
// Con burstGap en cero no hay encadenamiento. maxDuration acota una ráfaga que no termina
// —una parada con mucha gente—: al llegar al tope se cierra el clip y lo que sigue abre
// otro. Un maxDuration menor que duration se toma como duration.
func armarRafaga(cola []videoJob, preRoll, duration, burstGap, maxDuration time.Duration) rafaga {
	ancla := cola[0]
	post := duration - preRoll
	r := rafaga{
		indices: []int{0},
		desde:   ancla.when.Add(-preRoll),
		ultimo:  ancla.when,
	}
	r.hasta = r.desde.Add(duration)
	if maxDuration < duration {
		maxDuration = duration
	}
	for i := 1; i < len(cola); i++ {
		j := cola[i]
		if j.door != ancla.door || j.when.Before(r.desde) {
			continue
		}
		// La regla de siempre: dentro de la ventana, con cola suficiente.
		if !j.when.After(r.hasta.Add(-videoMinPostRoll)) {
			r.indices = append(r.indices, i)
			if j.when.After(r.ultimo) {
				r.ultimo = j.when
			}
			// Encadenando, todo paso del grupo se gana su cola completa, no solo los
			// videoMinPostRoll de la regla vieja. Sin encadenar la ventana queda fija,
			// como siempre.
			if fin := j.when.Add(post); burstGap > 0 && fin.After(r.hasta) &&
				fin.Sub(r.desde) <= maxDuration {
				r.hasta = fin
			}
			continue
		}
		// Encadenar: sigue la ráfaga y el clip no se pasa del tope.
		if burstGap > 0 && !j.when.After(r.ultimo.Add(burstGap)) &&
			j.when.Add(post).Sub(r.desde) <= maxDuration {
			r.indices = append(r.indices, i)
			r.ultimo = j.when
			if fin := j.when.Add(post); fin.After(r.hasta) {
				r.hasta = fin
			}
		}
	}
	return r
}

// SetRafaga encadena los pasos seguidos de una puerta en un solo clip (ver armarRafaga).
func (a *VideoActor) SetRafaga(burstGap, maxDuration time.Duration) *VideoActor {
	a.burstGap, a.maxDuration = burstGap, maxDuration
	return a
}

// next arranca la siguiente extracción si no hay ninguna en curso.
//
// Agrupa: el primer pendiente define la ventana y se absorben todos los demás de la
// misma puerta cuyo evento caiga dentro con al menos videoMinPostRoll de video después.
// Así una ráfaga de pasos comparte un clip en vez de bajar la misma ventana N veces —
// medido: la cámara ajusta el starttime al keyframe anterior, con lo que dos eventos del
// mismo GOP devuelven bytes idénticos.
func (a *VideoActor) next(ctx actor.Context) {
	if a.working || len(a.pending) == 0 || !a.camaraEncendida {
		return
	}

	anchor := a.pending[0]
	// La ráfaga se arma ANTES de esperar: su final, y no el del primer paso, es lo que
	// hay que dejar grabar.
	rafaga := armarRafaga(a.pending, a.preRoll, a.duration, a.burstGap, a.maxDuration)
	desde, hasta := rafaga.desde, rafaga.hasta

	// Esperar aquí, ANTES de extraer, a que la cámara haya grabado toda la ventana.
	// Además de ser necesario para que exista el video, deja que la cola se llene:
	// agrupar al recibir el primer evento daría un grupo de uno solo, porque los pasos
	// siguientes todavía no llegaron, y volverían los clips duplicados.
	//
	// Con ráfagas hay una segunda condición: no cerrar la ráfaga mientras pueda llegar un
	// paso que la continúe, o sea hasta burstGap después del último. Cada vez que se
	// despierta se vuelve a armar, así que un paso que llegó en el medio la alarga.
	//
	// Ojo con los dos relojes: `hasta` viene del evento, o sea del reloj de la CÁMARA,
	// mientras time.Until mide contra el reloj del GATEWAY. Si no coinciden, la espera
	// calculada no tiene sentido físico. Medido en campo: un gateway 19 meses atrasado
	// respecto de la cámara producía una espera de 19 meses y no se extraía nada nunca,
	// sin un solo error en el log — el conteo funcionaba y los clips simplemente no
	// aparecían.
	//
	// El máximo físico de esta espera es preRoll + la ventana + burstGap + settle. Más que
	// eso solo puede venir de una diferencia de relojes, así que se acota y se avisa.
	// Extraer de inmediato es seguro: la cámara indexa sus grabaciones con su propio
	// reloj, que es el mismo del evento, y HasCoverage verifica la cobertura antes de
	// pedir el tramo.
	espera := time.Until(hasta.Add(videoSettleDelay))
	if a.burstGap > 0 {
		if r := time.Until(rafaga.ultimo.Add(a.burstGap + time.Second)); r > espera {
			espera = r
		}
	}
	maxEspera := a.preRoll + a.duration + a.burstGap + videoSettleDelay + videoClockSlack
	if espera > maxEspera {
		if !a.clockWarned {
			a.clockWarned = true
			a.warnLog.Printf("el reloj del equipo y el de la cámara difieren en ~%v: la hora "+
				"del evento (%s) está en el futuro para este equipo. Se extrae igual, pero "+
				"revisá el reloj del gateway porque afecta el timestamp de los mensajes MQTT",
				espera.Round(time.Minute), anchor.when.Format(time.RFC3339))
		}
		espera = 0
	}
	// Un grupo que volvió a la cola porque el índice de la cámara iba atrasado espera su
	// turno de reintento. Va con el reloj del gateway, así que no la toca el acote de
	// arriba, que existe por la diferencia entre los dos relojes.
	if r := time.Until(anchor.noAntes); r > espera {
		espera = r
	}
	if espera > 0 {
		if !a.tickPend {
			a.tickPend = true
			self, system := ctx.Self(), ctx.ActorSystem()
			time.AfterFunc(espera, func() { system.Root.Send(self, &msgVideoTick{}) })
		}
		return
	}

	group := make([]videoJob, 0, len(rafaga.indices))
	enGrupo := make(map[int]bool, len(rafaga.indices))
	for _, i := range rafaga.indices {
		group = append(group, a.pending[i])
		enGrupo[i] = true
	}
	resto := a.pending[:0:0]
	for i, j := range a.pending {
		if !enGrupo[i] {
			resto = append(resto, j)
		}
	}
	ventana := hasta.Sub(desde)
	a.pending = resto
	a.working = true
	// El plazo de espera del índice corre desde el primer intento del grupo, y los
	// eventos que se suman en un reintento heredan el del ancla.
	if group[0].primerIntento.IsZero() {
		group[0].primerIntento = time.Now()
	}
	for i := range group {
		group[i].primerIntento = group[0].primerIntento
	}

	self := ctx.Self()
	system := ctx.ActorSystem()
	host := a.cameraFor(anchor.door)
	ident := a.camInfo[anchor.door]
	preferLocal := a.horaLocal[anchor.door]
	sinDescarga := a.sinDescarga[anchor.door]
	hostname := a.hostname

	dir := filepath.Join(a.dir, anchor.when.Local().Format("2006-01-02"))
	// El clip no lleva sufijo in/out: es de una puerta y una ventana de tiempo, y puede
	// contener entradas y salidas. El tipo pertenece a cada evento, o sea al sidecar.
	// El nombre es autosuficiente —fecha, hora, equipo, puerta y uid— para poder
	// buscarlo en un repositorio final donde se pierda la estructura de directorios.
	clip := uniquePath(dir, fmt.Sprintf("%s_%s_p%d_%s",
		anchor.when.Local().Format("20060102T150405"), hostname, anchor.door, anchor.uid), ".mp4")

	plazo := ventana*3 + videoSettleDelay + 30*time.Second
	cctx, cancel := context.WithTimeout(context.Background(), plazo)
	a.cancel = cancel

	go func() {
		defer cancel()
		done := &msgVideoDone{group: group, dest: clip}

		// El disco se mide antes de cada extracción. Tres desenlaces, y los tres se
		// informan: no poder medir NO detiene la extracción —dejar al vehículo sin video
		// porque falló un Statfs es peor que el riesgo que evita— pero sí queda avisado,
		// porque un fail-open silencioso no se puede auditar después.
		libre, errLibre := freeSpace(a.dir)
		switch {
		case errLibre != nil:
			done.medirErr = errLibre
		case a.minFree > 0 && libre < a.minFree:
			done.sinEspacio = true
			done.libre, done.espacioMedido = libre, true
			system.Root.Send(self, done)
			return
		default:
			done.libre, done.espacioMedido = libre, true
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

		// La búsqueda prueba UTC y hora local; sin eso la G1 nunca tenía cobertura y el
		// reintento en hora local de video.Extract no llegaba a correr.
		cov, err := cli.HasCoverage(cctx, videoTrack, desde, hasta,
			fmt.Sprintf("C0DE0001-0000-0000-0000-%012d", anchor.when.Unix()%1e12), preferLocal)
		if err != nil {
			done.err = fmt.Errorf("consultando grabaciones: %w", err)
			system.Root.Send(self, done)
			return
		}
		horaLocal := cov.HoraLocal
		if cov.Tramo != nil || cov.Pendiente {
			done.horaLocal, done.horaConocida = horaLocal, true
		}
		if cov.Tramo == nil {
			// La cámara viene grabando pero su índice no llega al final de la ventana:
			// se vuelve a preguntar más tarde en vez de perder el clip.
			if cov.Pendiente && time.Since(group[0].primerIntento) < videoIndiceEspera {
				done.reintentar, done.indexadoHasta = true, cov.IndexadoHasta
			} else {
				done.sinCobertura, done.indexadoHasta = true, cov.IndexadoHasta
			}
			system.Root.Send(self, done)
			return
		}

		// Primero la descarga HTTP de los archivos grabados, después el RTSP.
		//
		// El orden no es indiferente: en la G1 (V4.2.2) el RTSP de reproducción devuelve
		// un stream truncado e idéntico para cualquier instante, SIN error —el clip sale
		// verde y nada lo delata—, mientras la descarga entrega la grabación intacta. En
		// la G0 la descarga no existe y responde methodNotAllowed: eso se aprende y la
		// puerta queda en RTSP, que en ese modelo sí funciona.
		var res *video.Result
		if !sinDescarga && len(cov.Archivos) > 0 {
			archivos := make([]video.ArchivoGrabado, 0, len(cov.Archivos))
			for _, ar := range cov.Archivos {
				archivos = append(archivos, video.ArchivoGrabado{Desde: ar.Desde, PlaybackURI: ar.PlaybackURI})
			}
			res, err = video.ExtraerDescarga(cctx, video.Descarga{
				Abrir:    cli.Download,
				Archivos: archivos,
				Start:    desde, Duration: ventana, Dest: clip,
			})
			if errors.Is(err, isapi.ErrDescargaNoSoportada) {
				done.sinDescarga = true
				res, err = nil, nil
			}
		}
		if res == nil && err == nil {
			// La convención que encontró la grabación es la misma que entiende la
			// reproducción: se pasa resuelta y el recorte no paga el intento fallido.
			res, err = video.Extract(cctx, video.Request{
				Host: host, User: a.user, Pass: a.pass, Track: videoTrack,
				Start: desde, Duration: ventana, Dest: clip, HoraLocal: horaLocal,
			})
		}
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
			if err := writeSidecar(meta, j, hostname, host, ident, clip, desde, ventana, res); err != nil {
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
// (medido: 2 s a 20 fps en la G0, 4.2 s en la G1) porque el clip arranca en el keyframe
// anterior. Eso da más pre-roll que el configurado, nunca menos, pero no es el primer
// frame.
//
// OffsetS se mide contra el primer frame REAL cuando se conoce (video.Result.Inicio,
// siempre por descarga) y entonces es exacto. Por RTSP la cámara no dice dónde empezó y
// se mide contra el instante pedido: se queda corto en hasta un GOP. Medido el
// 2026-11-07 en la G1: con 10 s pedidos, el cruce estaba en el segundo 13.
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
	// MaxGapS es el hueco más largo entre dos fotos del clip, en segundos, y es la medida
	// de si el clip se ve fluido o congelado. A 20 fps lo normal es 0.05.
	//
	// Existe para poder juzgar un clip **sin abrirlo**: con H.264+ (SmartCodec) activo y
	// escena quieta la cámara deja de emitir fotos, y un clip de 10 segundos puede traer un
	// hueco de 5.7 s. La plataforma puede detectar de lejos una cámara así comparando este
	// campo contra `duration_s`.
	MaxGapS float64 `json:"max_gap_s"`
	// FPSEffective son las fotos por segundo que realmente trae el clip, que con H.264+ es
	// bastante menor que los fps configurados en la cámara.
	FPSEffective float64 `json:"fps_effective"`
	// Codec es el que la cámara entregó para este tramo, no el que tiene configurado hoy:
	// la SD conserva grabaciones de antes de un cambio de codec.
	Codec string `json:"codec,omitempty"`
}

// fpsEfectivo son las fotos por segundo que trae el clip. Devuelve 0 si no se puede medir,
// en vez de un infinito que rompería el JSON.
func fpsEfectivo(samples int, dur time.Duration) float64 {
	if dur <= 0 || samples <= 1 {
		return 0
	}
	return round2(float64(samples) / dur.Seconds())
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// inicioReal es el primer frame del clip si el extractor lo conoce, o el instante pedido.
func inicioReal(pedido time.Time, res *video.Result) time.Time {
	if res != nil && !res.Inicio.IsZero() {
		return res.Inicio
	}
	return pedido
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
		OffsetS:         job.when.Sub(inicioReal(start, res)).Seconds(),
		DurationS:       int(dur.Seconds()),
		Camera:          host,
		CameraSerial:    ident.serial,
		CameraMAC:       ident.mac,
		CamCounter:      job.counter,
		Samples:         res.Samples,
		Bytes:           res.Bytes,
		MaxGapS:         round2(res.MaxGap.Seconds()),
		FPSEffective:    fpsEfectivo(res.Samples, res.Duration),
		Codec:           res.Codec,
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
