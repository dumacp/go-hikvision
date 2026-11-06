package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/remote"
	"github.com/dumacp/go-hikvision/client"
	"github.com/dumacp/go-hikvision/client/parametros"
)

// puertoRemoteBase es desde dónde se busca un puerto libre para protoactor remote.
//
// No es 8088: ese es el servidor HTTP que recibe los eventos de la cámara. Y no es un puerto
// fijo porque en el gateway conviven varios binarios que hacen esto mismo y se pisarían;
// go-counterpass sondea desde 8099 por la misma razón.
const puertoRemoteBase = 8090

// puertoLibre devuelve el primer puerto de localhost donde nadie esté escuchando.
func puertoLibre(desde int) int {
	for p := desde; p < desde+50; p++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), 500*time.Millisecond)
		if err != nil {
			return p
		}
		conn.Close()
	}
	return desde
}

// arrancarParametros habilita la descarga de configuración desde la plataforma.
//
// Necesita protoactor REMOTE, que este binario no usaba: la configuración viaja desde
// nats-gwIot por un PID remoto, no por MQTT. El descubrimiento sí es por MQTT local.
//
// Nada de esto es terminal. Si el broker no está, si nats-gwIot no corre o si no hay sesión
// con la plataforma, el binario cuenta igual con los flags, los defaults y el cache en
// disco. Contar pasajeros no depende de poder reconfigurarse.
func arrancarParametros(sys *actor.ActorSystem, base client.Efectiva, fijados map[string]bool,
	destinos ...*actor.PID) {

	id := deviceID
	if len(id) == 0 {
		h, err := os.Hostname()
		if err != nil {
			warnlog.Printf("config: no se pudo leer el hostname y no se pasó -id: %s; "+
				"la configuración de plataforma queda deshabilitada", err)
			return
		}
		id = h
	}

	// El singleton de MQTT tiene que existir YA, y en ESTE sistema de actores.
	//
	// client.Subscribe llama a getInstance(nil), y ese nil hace que, si todavía no se
	// creó, el singleton se monte sobre un actor.NewActorSystem() propio. CountingActor lo
	// inicializa en su Started, pero eso corre en otra goroutine y no hay nada que
	// garantice que haya terminado cuando llegamos acá: el binario quedaría con dos
	// sistemas de actores y un cliente de MQTT huérfano, sin un solo error. InitPubSub es
	// idempotente (sync.Once), así que llamarlo de más no cuesta nada y vuelve el orden
	// determinista.
	if err := client.InitPubSub(sys.Root); err != nil {
		warnlog.Printf("config: pubsub no disponible: %s; la configuración de plataforma "+
			"se reintenta sola", err)
	}

	puerto := puertoLibre(puertoRemoteBase)
	r := remote.NewRemote(sys, remote.Configure("127.0.0.1", puerto))
	r.Start()

	act := parametros.NewActor(parametros.Config{
		ID:       id,
		Cache:    platformConfigCache,
		Base:     base,
		Fijados:  fijados,
		Destinos: destinos,
	})
	act.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).SetLogBuild(buildlog)
	if debug {
		act.WithDebug()
	}
	if _, err := sys.Root.SpawnNamed(actor.PropsFromFunc(act.Receive), "parametros"); err != nil {
		errlog.Printf("config: no se pudo crear el actor de parámetros: %s", err)
		return
	}
	infolog.Printf("config: configuración de plataforma habilitada; id=%q, atributo "+
		"DEV_CAMERACOUNT_CONFIG, respuesta de descubrimiento en %q, remote en 127.0.0.1:%d, "+
		"cache %q", id, parametros.TopicReply, puerto, platformConfigCache)
}
