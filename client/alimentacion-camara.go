package client

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/dumacp/pubsub"
)

// MsgCameraPower le dice a los actores que le hablan a la cámara si pueden hacerlo.
//
// Existe porque apagar la cámara a propósito, sin avisarles, haría que los tres
// empiecen a reportar averías que no existen: PingActor publicaría un
// CounterDisconnected cada 300 s, CameraActor fallaría su ciclo de ISAPI cada media
// hora, y VideoActor intentaría extraer por RTSP de un equipo sin energía. Una alarma
// que miente es peor que no tenerla, porque entrena a ignorar la que sí importa.
type MsgCameraPower struct {
	On bool
}

// msgCortarAlimentacion lo dispara el temporizador del apagado: es el momento de
// accionar la GPIO, ya dado el plazo para que termine el clip en curso.
type msgCortarAlimentacion struct{}

// msgCamaraLista lo dispara el temporizador del arranque: la cámara ya tuvo tiempo de
// bootear y se le puede volver a hablar.
type msgCamaraLista struct{}

// SetCameraPower habilita el apagado de la cámara por GPIO comandado por go-ignition.
//
// Sin esta llamada —o sea, sin -cameraPowerGPIO— no se crea el actor de ignición, no se
// escucha el tópico y nada de esto corre. La salida ya viene en el nivel de "encendida",
// porque NuevaSalidaGPIO fija dirección y valor en una sola operación.
func (a *CountingActor) SetCameraPower(salida *SalidaGPIO, nivelApagado int, esperaApagado, esperaArranque time.Duration) {
	a.alimentacion = salida
	a.nivelApagado = nivelApagado
	a.esperaApagado = esperaApagado
	a.esperaArranque = esperaArranque
	a.camaraEncendida = true
}

// nivelEncendido es el complemento del nivel de apagado. Se deriva en vez de
// configurarse aparte para que no exista la combinación imposible de dos flags que
// digan lo mismo.
func (a *CountingActor) nivelEncendido() int {
	if a.nivelApagado == 0 {
		return 1
	}
	return 0
}

// aplicarAlimentacion reacciona al estado del vehículo.
func (a *CountingActor) aplicarAlimentacion(ctx actor.Context, msg *MsgIgnition) {
	if a.alimentacion == nil {
		return
	}
	if msg.Tipo != tipoSuspendEvt {
		// El hijo ya filtra; esto es la red por si alguien le manda otra cosa.
		return
	}
	if msg.Encendido == a.camaraEncendida {
		// go-ignition reemite el estado con cada evento de llave y al suscribirse. Sin
		// esta guarda, cada repetición de un UP reiniciaría la espera de arranque y la
		// cámara quedaría marcada como "no disponible" indefinidamente, sin contar a
		// nadie mientras tanto.
		a.buildLog.Printf("ignition: la camara ya estaba encendida=%v, sin cambios", msg.Encendido)
		return
	}
	a.camaraEncendida = msg.Encendido

	if !msg.Encendido {
		// Primero se calla a los tres y recién después se corta. Al revés, el plazo que
		// le damos al clip en curso serían también 20 segundos de keep-alive fallando
		// contra una cámara que ya no está.
		a.infoLog.Printf("ignition: vehiculo apagado, suspendiendo el dialogo con la "+
			"camara y cortando la alimentacion en %v (gpio %d -> %d)",
			a.esperaApagado, a.alimentacion.Pin(), a.nivelApagado)
		a.avisarAlimentacion(ctx, false)
		a.tras(ctx, a.esperaApagado, &msgCortarAlimentacion{})
		return
	}

	a.infoLog.Printf("ignition: vehiculo encendido, devolviendo alimentacion a la "+
		"camara (gpio %d -> %d) y esperando %v a que arranque",
		a.alimentacion.Pin(), a.nivelEncendido(), a.esperaArranque)
	if err := a.escribirLinea(a.nivelEncendido()); err != nil {
		// Sin energía no hay conteo, así que esto es un ERROR y no un WARN. No se
		// reintenta acá: el próximo evento de ignición lo vuelve a intentar, y
		// mientras tanto el log y el evento dicen qué pasó.
		a.errLog.Printf("ignition: no se pudo devolver la alimentacion a la camara: %s", err)
		a.publicarAlimentacion("power_error")
		return
	}
	a.tras(ctx, a.esperaArranque, &msgCamaraLista{})
}

// cortarAlimentacion acciona la GPIO. Llega por temporizador, así que puede estar
// obsoleto: si el vehículo volvió a encender mientras corría el plazo, se descarta.
func (a *CountingActor) cortarAlimentacion() {
	if a.alimentacion == nil || a.camaraEncendida {
		a.buildLog.Println("ignition: corte obsoleto, el vehiculo volvio a encender")
		return
	}
	if err := a.escribirLinea(a.nivelApagado); err != nil {
		a.errLog.Printf("ignition: no se pudo cortar la alimentacion de la camara: %s", err)
		a.publicarAlimentacion("power_error")
		return
	}
	a.infoLog.Printf("ignition: alimentacion de la camara cortada (gpio %d = %d)",
		a.alimentacion.Pin(), a.nivelApagado)
	a.publicarAlimentacion("poweroff")
}

// camaraTerminoDeArrancar rehabilita el diálogo. También puede llegar obsoleto, si el
// vehículo se apagó durante el arranque de la cámara.
func (a *CountingActor) camaraTerminoDeArrancar(ctx actor.Context) {
	if a.alimentacion == nil || !a.camaraEncendida {
		a.buildLog.Println("ignition: arranque obsoleto, el vehiculo se volvio a apagar")
		return
	}
	a.infoLog.Println("ignition: la camara ya tuvo tiempo de arrancar, se reanuda el dialogo")
	a.avisarAlimentacion(ctx, true)
	a.publicarAlimentacion("poweron")
}

// escribirLinea acciona la GPIO distinguiendo los dos fracasos posibles.
//
// Que la línea lea distinto de lo escrito NO es lo mismo que no poder manejarla: con el
// pad sin cablear, en i.MX el `value` de una salida lee el nivel eléctrico real y da 0
// hagamos lo que hagamos (ver ErrNivelNoVerificado). Tratarlo como fallo publicaría un
// power_error en cada ciclo de llave por un cable que todavía no está. Se avisa una vez
// por transición en WARN y se sigue, porque la escritura SÍ se hizo.
func (a *CountingActor) escribirLinea(nivel int) error {
	err := a.alimentacion.Escribir(nivel)
	if errors.Is(err, ErrNivelNoVerificado) {
		a.warnLog.Printf("ignition: %s; la escritura se hizo, pero no se puede confirmar "+
			"el nivel hasta que el rele este conectado", err)
		return nil
	}
	return err
}

// avisarAlimentacion reparte el estado a los tres hijos que le hablan a la cámara.
func (a *CountingActor) avisarAlimentacion(ctx actor.Context, encendida bool) {
	msg := &MsgCameraPower{On: encendida}
	for _, pid := range []*actor.PID{a.ping, a.camera, a.video} {
		if pid != nil {
			ctx.Send(pid, msg)
		}
	}
}

// publicarAlimentacion avisa a la plataforma. Sin esto, un equipo con la cámara apagada
// a propósito es indistinguible de uno con la cámara averiada: las dos dejan de contar
// y las dos se quedan calladas. Va una publicación por transición, con el mismo patrón
// que storage/storage_ok y videodir/videodir_ok del CAMERATIME.
func (a *CountingActor) publicarAlimentacion(estado string) {
	val := struct {
		Type   string `json:"type"`
		Status string `json:"status"`
		GPIO   int    `json:"gpio"`
		Level  int    `json:"level"`
		Source string `json:"source"`
	}{
		Type:   "CAMERA",
		Status: estado,
		GPIO:   a.alimentacion.Pin(),
		Level:  a.nivelApagado,
		Source: "ignition",
	}
	data, err := json.Marshal(&pubsub.Message{
		Timestamp: float64(time.Now().UnixNano()) / 1000000000,
		Type:      "CAMERAPOWER",
		Value:     val,
	})
	if err != nil {
		a.errLog.Printf("armando CAMERAPOWER: %s", err)
		return
	}
	a.buildLog.Printf("%s", data)
	if a.publicar != nil {
		a.publicar(topicAddEvents, data)
		return
	}
	Publish(topicAddEvents, data)
}

// tras manda un mensaje al propio actor después de d. Mismo mecanismo que
// CameraActor.schedule: el trabajo con espera nunca ocurre dentro de Receive.
//
// Se desvía por el campo `programar` para que las pruebas puedan ejercitar la política
// de apagado sin levantar un sistema de actores con su proveedor de persistencia. Lo
// que hay que poder probar es que un corte no se ejecute cuando el vehículo ya volvió a
// encender, y eso no debería exigir una boltdb.
func (a *CountingActor) tras(ctx actor.Context, d time.Duration, msg interface{}) {
	if a.programar != nil {
		a.programar(ctx, d, msg)
		return
	}
	self, system := ctx.Self(), ctx.ActorSystem()
	time.AfterFunc(d, func() { system.Root.Send(self, msg) })
}
