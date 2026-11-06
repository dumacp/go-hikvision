package client

import (
	"errors"

	"github.com/asynkron/protoactor-go/actor"
)

// Aplicación en caliente de la configuración que llega de la plataforma.
//
// El estado del conteo NO se toca acá: los acumulados, la boltdb y la persistencia viven en
// CountingActor, que no se reinicia. Lo que se reconstruye son sus HIJOS, y solo los que el
// cambio afecta. Un cambio de `videoFrameRate` no puede costar un contador.

// MsgAplicarConfig lleva una configuración ya resuelta y validada. La manda el actor de
// parámetros; CountingActor decide qué rehacer con ella.
type MsgAplicarConfig struct {
	Cfg Efectiva
}

// MsgSetCameras le pasa a ListenActor la lista de cámaras por puerta. Es lo que decide a qué
// contador va cada evento entrante, así que tiene que cambiar junto con el resto.
type MsgSetCameras struct {
	Cameras []string
}

// MsgRegistrarListen le dice a CountingActor dónde está ListenActor.
//
// Hace falta porque el orden de arranque es al revés: main spawnea `counting` primero —
// ListenActor necesita su PID para construirse— así que counting no puede conocerlo hasta
// después.
type MsgRegistrarListen struct {
	PID *actor.PID
}

// SetConfig fija la configuración inicial, la que salió de los flags, los defaults y el
// cache en disco. Se llama ANTES de spawnear el actor.
func (a *CountingActor) SetConfig(cfg Efectiva, fijados map[string]bool) {
	a.cfg = cfg.Copia()
	a.fijados = fijados
	a.pingAddr = a.cfg.PrimeraCamara()
	a.aplicarPuertas()
}

// SetVideoFactory y SetCameraFactory reemplazan a las Props fijas.
//
// Antes la configuración quedaba congelada dentro de unas Props armadas en main, y
// reconstruir el actor con otros valores era imposible sin reiniciar el proceso. La fábrica
// devuelve nil cuando ese trabajo no se puede hacer —sin directorio, sin cámaras o sin
// credenciales—, y entonces el hijo simplemente no existe, que es el comportamiento de
// siempre.
func (a *CountingActor) SetVideoFactory(f func(Efectiva) *actor.Props)  { a.videoFactory = f }
func (a *CountingActor) SetCameraFactory(f func(Efectiva) *actor.Props) { a.cameraFactory = f }

// Config devuelve la configuración aplicada.
func (a *CountingActor) Config() Efectiva { return a.cfg }

// aplicarPuertas vuelca la polaridad y el conteo con puerta cerrada en los mapas que usa el
// gating. Se hace EN EL LUGAR, sin reconstruir nada: son dos lecturas de mapa en la ruta del
// conteo y no hay ningún actor que dependa de ellas para existir.
//
// Una puerta que desaparece de la configuración conserva su valor anterior. Es deliberado:
// borrarla la dejaría con el default del mapa vacío —`openState` ausente vale 1— y eso
// puede invertir el gating de una puerta que simplemente no se mencionó.
func (a *CountingActor) aplicarPuertas() {
	for id, v := range a.cfg.ZeroOpenState {
		a.SetZeroOpenState(id, v)
	}
	for id, v := range a.cfg.CountWithCloseDoor {
		a.SetCountCloseDoor(id, v)
	}
}

// aplicarConfig es el punto de entrada de una configuración nueva.
func (a *CountingActor) aplicarConfig(ctx actor.Context, nueva Efectiva) {
	cambios := a.cfg.Diff(nueva)
	if !cambios.Hay() {
		a.buildLog.Println("config: la configuracion de plataforma no cambia nada")
		return
	}
	a.infoLog.Printf("config: configuracion nueva de plataforma, cambia %s", cambios.Describir())

	anterior := a.cfg
	a.cfg = nueva.Copia()
	a.pingAddr = a.cfg.PrimeraCamara()

	if cambios.Puertas || cambios.Camaras {
		a.aplicarPuertas()
	}
	if cambios.GPIO {
		a.reconfigurarAlimentacion(ctx, anterior)
	}
	if cambios.Camaras {
		if a.listen != nil {
			ctx.Send(a.listen, &MsgSetCameras{Cameras: a.cfg.Camaras})
		}
		a.reemplazarHijo(ctx, "ping", &a.ping, a.propsPing())
	}
	if cambios.Camaras || cambios.Camara {
		a.reemplazarHijo(ctx, "camera", &a.camera, a.propsCamera())
	}
	if cambios.Camaras || cambios.Video {
		a.reemplazarHijo(ctx, "video", &a.video, a.propsVideo())
	}
	a.infoLog.Printf("config: configuracion efectiva\n%s", a.cfg.Describir(a.fijados))
}

// nuevoPing arma el actor del keep-alive con la cámara vigente. Va separado de propsPing
// para poder verificar a qué dirección va a consultar sin levantar un sistema de actores:
// equivocarse acá no da error, da un CounterDisconnected falso cada 300 s contra una cámara
// que está contando sin problema.
func (a *CountingActor) nuevoPing() *PingActor {
	return &PingActor{addr: a.cfg.PrimeraCamara()}
}

func (a *CountingActor) propsPing() *actor.Props {
	ping := a.nuevoPing()
	return actor.PropsFromProducer(func() actor.Actor { return ping })
}

func (a *CountingActor) propsCamera() *actor.Props {
	if a.cameraFactory == nil {
		return nil
	}
	return a.cameraFactory(a.cfg)
}

// propsVideo devuelve nil cuando la extracción está apagada. `videoEnabled` se consulta acá
// y no dentro de la fábrica para que el interruptor de la plataforma sea una sola decisión,
// visible en un solo lugar.
func (a *CountingActor) propsVideo() *actor.Props {
	if a.videoFactory == nil || !a.cfg.VideoEnabled {
		return nil
	}
	return a.videoFactory(a.cfg)
}

// reemplazarHijo envenena el hijo y lo vuelve a crear con la configuración nueva.
//
// Tres cosas que no son obvias:
//
//   - `PoisonFuture().Wait()` bloquea el hilo de CountingActor hasta que el hijo termine.
//     Los eventos que lleguen mientras tanto esperan en el buzón, no se pierden; es el mismo
//     compromiso que hacen los otros binarios de la flota.
//   - props nil significa "este hijo no va": es como se apaga el video o el mantenimiento
//     de cámara sin tener un camino aparte para apagarlos.
//   - un hijo recién creado nace creyendo que la cámara está encendida. Si estaba apagada
//     por ignición hay que decírselo, o se pondría a hablarle a una cámara sin energía y a
//     reportar averías que no existen — justo lo que MsgCameraPower vino a evitar.
//
// Un fallo acá NO entra en pánico. CountingActor es el actor de persistencia: reiniciarlo
// replica la boltdb y republica acumulados recalculados, que es un precio desproporcionado
// por no haber podido reconstruir un accesorio.
func (a *CountingActor) reemplazarHijo(ctx actor.Context, nombre string, pid **actor.PID, props *actor.Props) {
	if *pid != nil {
		ctx.PoisonFuture(*pid).Wait()
		*pid = nil
	}
	if props == nil {
		a.infoLog.Printf("config: el actor %q queda deshabilitado", nombre)
		return
	}
	nuevo, err := ctx.SpawnNamed(props, nombre)
	if err != nil {
		a.errLog.Printf("config: no se pudo recrear el actor %q: %s", nombre, err)
		return
	}
	*pid = nuevo
	if !a.camaraEncendida {
		ctx.Send(nuevo, &MsgCameraPower{On: false})
	}
	a.infoLog.Printf("config: actor %q recreado", nombre)
}

// reconfigurarAlimentacion mueve la línea de apagado de la cámara en caliente.
//
// Es el único parámetro que mueve hardware, así que el orden importa: PRIMERO se devuelve la
// línea vieja al nivel de "encendida" y recién después se toma la nueva. Al revés quedaría
// una línea abandonada manejando el nivel de apagado, y con el relé conectado eso es una
// cámara que no vuelve nunca.
//
// Si algo falla se conserva la configuración de alimentación anterior y se registra ERROR. A
// diferencia del arranque —donde un -cameraPowerGPIO malo detiene el binario— acá ya estamos
// contando pasajeros, y el conteo no se negocia por el cableado de un accesorio.
func (a *CountingActor) reconfigurarAlimentacion(ctx actor.Context, anterior Efectiva) {
	nueva := a.cfg

	// Nivel seguro para soltar la línea vieja: el que corresponda al estado actual del
	// vehículo. Si la cámara está apagada a propósito, devolverle energía acá la
	// encendería sola.
	soltarLineaVieja := func() {
		if a.alimentacion == nil {
			return
		}
		nivel := a.nivelEncendido()
		if !a.camaraEncendida {
			nivel = a.nivelApagado
		}
		if err := a.escribirLinea(nivel); err != nil {
			a.errLog.Printf("config: no se pudo dejar la gpio %d en %d antes de soltarla: %s",
				a.alimentacion.Pin(), nivel, err)
		}
	}

	if nueva.CameraPowerGPIO <= 0 {
		if a.alimentacion == nil {
			return
		}
		a.warnLog.Printf("config: se deshabilita el apagado por gpio (antes era la %d); "+
			"la linea queda en el nivel actual y nadie la vuelve a tocar", anterior.CameraPowerGPIO)
		soltarLineaVieja()
		a.alimentacion = nil
		a.reemplazarHijo(ctx, "ignition", &a.ignition, nil)
		return
	}

	mismaLinea := a.alimentacion != nil && a.alimentacion.Pin() == nueva.CameraPowerGPIO
	if mismaLinea {
		// Solo cambió la polaridad. Se reescribe el nivel que corresponde al estado
		// actual, con el significado nuevo.
		a.nivelApagado = nueva.CameraPowerOffLevel
		nivel := a.nivelEncendido()
		if !a.camaraEncendida {
			nivel = a.nivelApagado
		}
		if err := a.escribirLinea(nivel); err != nil {
			a.errLog.Printf("config: no se pudo aplicar el nivel de apagado nuevo (%d) en la "+
				"gpio %d: %s", nueva.CameraPowerOffLevel, nueva.CameraPowerGPIO, err)
			a.nivelApagado = anterior.CameraPowerOffLevel
			return
		}
		a.infoLog.Printf("config: nivel de apagado de la gpio %d ahora es %d",
			nueva.CameraPowerGPIO, a.nivelApagado)
		return
	}

	// Cambia la línea. Primero soltar la vieja, después tomar la nueva.
	soltarLineaVieja()

	nivelInicial := 1
	if nueva.CameraPowerOffLevel == 1 {
		nivelInicial = 0
	}
	if !a.camaraEncendida {
		nivelInicial = nueva.CameraPowerOffLevel
	}
	salida, err := NuevaSalidaGPIO(nueva.CameraPowerGPIO, nivelInicial)
	if err != nil && !errors.Is(err, ErrNivelNoVerificado) {
		a.errLog.Printf("config: no se pudo tomar la gpio %d: %s; se conserva la "+
			"configuracion de alimentacion anterior", nueva.CameraPowerGPIO, err)
		a.cfg.CameraPowerGPIO = anterior.CameraPowerGPIO
		a.cfg.CameraPowerOffLevel = anterior.CameraPowerOffLevel
		return
	}
	if err != nil {
		a.warnLog.Printf("config: gpio %d: %s; se continua", nueva.CameraPowerGPIO, err)
	}
	a.alimentacion = salida
	a.nivelApagado = nueva.CameraPowerOffLevel
	a.infoLog.Printf("config: apagado de camara ahora por la gpio %d, apagada=%d, encendida=%d",
		nueva.CameraPowerGPIO, a.nivelApagado, a.nivelEncendido())
	a.asegurarIgnition(ctx)
}

// asegurarIgnition crea el hijo que escucha a go-ignition si hace falta y todavía no existe.
func (a *CountingActor) asegurarIgnition(ctx actor.Context) {
	if a.alimentacion == nil || a.ignition != nil {
		return
	}
	ign := NewIgnitionActor()
	ign.SetLogError(a.errLog).
		SetLogWarn(a.warnLog).
		SetLogInfo(a.infoLog).
		SetLogBuild(a.buildLog)
	if a.debug {
		ign.WithDebug()
	}
	props := actor.PropsFromProducer(func() actor.Actor { return ign })
	pid, err := ctx.SpawnNamed(props, "ignition")
	if err != nil {
		a.errLog.Printf("config: no se pudo crear el actor de ignicion: %s", err)
		return
	}
	a.ignition = pid
}
