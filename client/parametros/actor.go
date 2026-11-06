// Package parametros baja la configuración del binario desde la plataforma.
//
// El binario NO habla con la plataforma. Habla con nats-gwIot, un proceso vecino del mismo
// gateway que tiene las credenciales y la conexión NATS:
//
//	Plataforma ── NATS JetStream KV, bucket "<orgId>_CFG-PARAMS", key = hostname
//	     │
//	nats-gwIot ── descubrimiento por MQTT local, datos por protoactor remote en 127.0.0.1
//	     │
//	este paquete ── DEV_CAMERACOUNT_CONFIG ── CountingActor
//
// Es un watch con push: cada revisión del KV llega sola, no hay sondeo. Hay un retardo
// inicial de ~10 s antes del primer descubrimiento, así que el binario SIEMPRE arranca con
// los flags, los defaults y el cache en disco, y la plataforma llega después.
package parametros

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client"
	apiparams "github.com/dumacp/go-params/api/params"
	"github.com/dumacp/go-params/pkg/params"
)

// TopicReply es el tópico de MQTT local donde nats-gwIot nos contesta el descubrimiento.
// Es propio de este binario, como "dconsolediscovery/params" lo es de go-driverconsole: si
// dos procesos comparten el tópico, cada uno recibe las respuestas del otro.
const TopicReply = "cameracountdiscovery/params"

// Config es lo que el actor necesita para resolver una configuración nueva.
type Config struct {
	// ID es la llave del KV de la plataforma. Vacío usa el hostname del gateway.
	ID string
	// Cache es el archivo donde se guarda el último JSON bueno. Vacío lo deshabilita.
	Cache string
	// Base es la configuración que salió de los flags y los defaults: lo que la
	// plataforma va a rellenar.
	Base client.Efectiva
	// Fijados son los flags pasados explícitamente en la línea de comandos. Esos le ganan
	// a la plataforma; ver client.Resolver.
	Fijados map[string]bool
	// Destinos reciben un client.MsgAplicarConfig con la configuración ya resuelta.
	Destinos []*actor.PID
}

// Actor escucha a la plataforma y reparte la configuración resuelta.
type Actor struct {
	*client.Logger
	cfg Config
	// ultimaRaw es el JSON crudo ya aplicado. Sirve para no rehacer trabajo cuando
	// nats-gwIot reenvía la misma revisión, que pasa en cada reconexión.
	ultimaRaw string
}

// NewActor crea el actor. Va como hijo de la raíz, al lado de counting y listenner.
func NewActor(cfg Config) *Actor {
	a := &Actor{cfg: cfg}
	a.Logger = &client.Logger{}
	return a
}

func (a *Actor) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		a.InitLogs()
		a.LogInfo().Printf("actor started \"%s\"", ctx.Self().Id)

		disc := &discoveryActor{
			topicReply: TopicReply,
			sub:        client.Subscribe,
			pub:        client.Publish,
			avisar:     a.LogWarn().Printf,
		}
		// La persistencia de go-params se deshabilita a propósito (el tercer argumento).
		// Su cache escribe el mapa plano crudo y lo lee como otro esquema, así que
		// siempre recupera un struct vacío, y además nunca lo propaga al padre. Un
		// arranque sin red queda sin configuración aunque el archivo esté ahí. Acá se
		// usa un cache propio; ver LeerCache/guardarCache.
		hijo := params.Actor(a.cfg.ID, "", true, params.NatsActor(a.cfg.ID, disc))
		if _, err := ctx.SpawnNamed(actor.PropsFromFunc(hijo.Receive), "params-actor"); err != nil {
			// No es terminal: sin configuración de plataforma el binario cuenta igual,
			// con los flags y el cache. Tumbar el conteo por esto sería desproporcionado.
			a.LogError().Printf("params: no se pudo crear el cliente de parametros: %s", err)
		}

	case *actor.Stopping:
		a.LogWarn().Printf("stopping \"%s\"", ctx.Self().Id)

	case *apiparams.Parameters:
		a.recibir(ctx, msg)
	}
}

// recibir procesa el mapa de parámetros y se queda solo con lo nuestro.
func (a *Actor) recibir(ctx actor.Context, p *apiparams.Parameters) {
	raw := strings.TrimSpace(p.GetCameracountConf())
	if len(raw) == 0 {
		// Vacío NO es un error ni una orden de volver a los defaults: es un equipo al que
		// todavía no le cargaron el atributo. Pisar la configuración vigente acá dejaría
		// a un vehículo configurado contando con los valores de fábrica.
		a.LogWarn().Println("params: DEV_CAMERACOUNT_CONFIG vacio, se conserva la " +
			"configuracion vigente")
		return
	}
	if raw == a.ultimaRaw {
		a.LogBuild().Println("params: misma configuracion que la ya aplicada")
		return
	}

	cfg, err := client.ParseConfiguracion([]byte(raw))
	if err != nil {
		// Se conserva la anterior, entera. Una configuración a medias deja el equipo en
		// un estado que nadie escribió.
		a.LogError().Printf("params: DEV_CAMERACOUNT_CONFIG invalido (%s); se conserva la "+
			"configuracion vigente. JSON recibido: %s", err, raw)
		return
	}
	a.ultimaRaw = raw
	if err := guardarCache(a.cfg.Cache, []byte(raw)); err != nil {
		a.LogWarn().Printf("params: no se pudo guardar el cache en %q: %s", a.cfg.Cache, err)
	}

	efectiva := client.Resolver(a.cfg.Base, cfg, a.cfg.Fijados)
	a.LogInfo().Printf("params: configuracion recibida de plataforma")
	for _, pid := range a.cfg.Destinos {
		if pid != nil {
			ctx.Send(pid, &client.MsgAplicarConfig{Cfg: efectiva})
		}
	}
}

// LeerCache devuelve la última configuración buena guardada en disco.
//
// Existe porque el cache de go-params no funciona —escribe un esquema y lee otro, y nunca
// propaga lo leído—, y sin esto un arranque sin red deja el equipo con los defaults. Con la
// prioridad invertida de este binario eso es peligroso: un lanzador que delega la
// configuración a la plataforma no trae los flags, y el default de zeroOpenState descarta
// TODOS los cruces en un vehículo cableado al revés.
//
// Devuelve (nil, nil) cuando no hay cache: no tenerlo es lo normal el primer día.
func LeerCache(ruta string) (*client.Configuracion, error) {
	if len(strings.TrimSpace(ruta)) == 0 {
		return nil, nil
	}
	data, err := os.ReadFile(ruta)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := client.ParseConfiguracion(data)
	if err != nil {
		return nil, fmt.Errorf("cache %q: %w", ruta, err)
	}
	return cfg, nil
}

// guardarCache escribe como .part y renombra. El rename es atómico, así que un corte de
// energía en mitad de la escritura deja el cache anterior intacto en vez de uno truncado —
// que es el peor caso posible: una configuración ilegible en el arranque siguiente.
func guardarCache(ruta string, raw []byte) error {
	if len(strings.TrimSpace(ruta)) == 0 {
		return nil
	}
	if dir := filepath.Dir(ruta); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := ruta + ".part"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ruta)
}

// Marca deja constancia de cuándo se guardó el cache, para el diagnóstico en campo.
func Marca(ruta string) string {
	fi, err := os.Stat(ruta)
	if err != nil {
		return "sin cache"
	}
	return fi.ModTime().Format(time.RFC3339)
}
