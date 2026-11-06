package parametros

import (
	"encoding/json"
	"fmt"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/dumacp/go-gwiot/pkg/gwiotmsg"
	"github.com/dumacp/go-gwiot/pkg/gwiotmsg/gwiot"
)

// Descubrimiento del proceso nats-gwIot por MQTT local.
//
// Esto es una REIMPLEMENTACIÓN de go-gwiot/pkg/gwiot.NewDiscoveryActor, y la copia es
// deliberada. Ese actor son cuarenta líneas, pero vive en el mismo paquete Go que el cliente
// completo de NATS: importarlo enlaza nats.go, go-oidc, oauth2 y keycloak dentro de un
// binario que NUNCA abre una conexión a NATS —de eso se encarga nats-gwIot, que corre
// aparte y tiene las credenciales—. Este repo ya descartó ffmpeg por 1.8 MB en armv7; esto
// cuesta bastante más que eso por código que no se ejecuta.
//
// Lo único que se importa es gwiotmsg (protobuf puro) y gwiotmsg/gwiot (dos constantes), así
// que el protocolo sigue siendo el mismo y compartido: si cambia, cambia el .proto.
//
// El protocolo, completo:
//
//	nosotros  -> GWIOT/discovery   {"reply":"cameracountdiscovery/params"}
//	nats-gwIot -> cameracountdiscovery/params  {"host":"127.0.0.1","port":8090}
//
// y con ese host:port se activa un actor remoto por protoactor.
type discoveryActor struct {
	topicReply string
	sub        func(topic string, pid *actor.PID, parse func([]byte) interface{}) error
	pub        func(topic string, msg []byte)
	avisar     func(formato string, args ...interface{})
}

// parseDiscoveryResponse devuelve el mensaje o un error COMO VALOR, que es la convención de
// esta familia de suscripciones.
func parseDiscoveryResponse(m []byte) interface{} {
	ev := &gwiotmsg.DiscoveryResponse{}
	if err := json.Unmarshal(m, ev); err != nil {
		return fmt.Errorf("respuesta de descubrimiento ilegible: %w", err)
	}
	return ev
}

func (a *discoveryActor) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		if err := a.sub(a.topicReply, ctx.Self(), parseDiscoveryResponse); err != nil {
			// No es terminal: el padre reintenta el descubrimiento cada minuto, y
			// SubscribeFun deja la suscripción anotada para cuando el cliente de MQTT
			// conecte. Entrar en pánico acá tumbaría el árbol por un broker que todavía
			// no levantó.
			a.avisar("params: no se pudo suscribir a %q: %s", a.topicReply, err)
		}
	case *gwiotmsg.Discovery:
		data, err := json.Marshal(&gwiotmsg.Discovery{Reply: a.topicReply})
		if err != nil {
			a.avisar("params: armando el descubrimiento: %s", err)
			break
		}
		a.pub(gwiot.SUBS_TOPIC, data)
	case *gwiotmsg.DiscoveryResponse:
		if ctx.Parent() != nil {
			ctx.Send(ctx.Parent(), msg)
		}
	}
}
