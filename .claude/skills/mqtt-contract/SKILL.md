---
name: mqtt-contract
description: Contrato MQTT del binario go-hikvision — tópicos publicados y suscritos, forma exacta del JSON de cada mensaje y quién los consume. Úsala antes de cambiar un tópico, agregar un campo al payload, o depurar por qué la plataforma no ve conteos o estados de puerta.
---

# Contrato MQTT de go-hikvision

Broker: **`tcp://127.0.0.1:1883`** (local, sin credenciales), `clientID` = `camera-<rand>-<unix>`,
keepalive 30s, autoreconexión activa. Todo en [client/pubsub.go](../../../client/pubsub.go).

Este contrato es una **frontera con otros servicios del gateway**. Cambiar un nombre de tópico o
la forma de un payload rompe consumidores que no viven en este repo: trátalo como cambio
contractual, no como refactor. Si lo cambias, actualiza esta skill en el mismo commit.

## Publica

| Tópico | Constante | Cuándo | Contenido |
|---|---|---|---|
| `COUNTERSMAPDOOR` | `topicCounter` | cada 45 s y tras snapshot/replay | acumulados por puerta |
| `EVENTS/backcounter` | `topicEvents` | por cada paso validado, y al perder la cámara | `COUNTERSDOOR` / `CounterDisconnected` |
| `EVENTS/counterevents` | `topicAddEvents` | por cada tamper, por cada clip escrito, al corregir o detectar deriva en el reloj de una cámara, y al cortarle o devolverle la energía | `TAMPERING`, `COUNTERSDOORVIDEO`, `CAMERATIME`, `CAMERAPOWER` |
| `DOORS` | — | al arrancar `DoorsActor` | pide que le publiquen estados de puerta |

`QoS 0`, sin retain, timeout de publicación 3 s.

### `COUNTERSMAPDOOR` — registros acumulados

`registerMap` de [client/registermap.go](../../../client/registermap.go), plano, sufijo = `id` de
puerta (`0` frontal, `1` trasera):

```json
{"inputs0":0,"inputs1":123,"outputs0":0,"outputs1":118,
 "anomalies0":0,"anomalies1":0,"tampering0":0,"tampering1":0}
```

- `anomalies*` siempre va en `0`: `MsgSendRegisters` pasa un mapa vacío. Está reservado.
- **No se publica nada si `inputs` y `outputs` suman 0**: es intencional, evita reportar ceros
  cuando la base persistida aún no se recuperó y así no se pisan contadores válidos en la
  plataforma ([counting-actor.go:165](../../../client/counting-actor.go#L165)).
- Solo dos puertas están modeladas en el JSON. Un tercer `id` se contaría internamente pero
  **no aparecería aquí**: agregar puerta obliga a extender `registerMap` y a coordinar con el
  consumidor.

### `EVENTS/backcounter` — pasos y desconexión

Envoltura `pubsub.Message`: `timestamp` (segundos epoch, float), `type`, `value`.

Paso validado (`type: "COUNTERSDOOR"`), de `buildEventPass`:

```json
{"timestamp":1723406400.123,"type":"COUNTERSDOOR",
 "value":{"coord":"$GPRMC,...","id":1,"state":1,"counters":[2,0],"type":"CAMERA",
          "event_id":"9668c0b7"}}
```

- `counters` = `[entradas, salidas]` — **incrementos** de este evento, no acumulados.
- `event_id` identifica este paso de forma inequívoca y es la llave para unirlo con su video, que
  llega después en `COUNTERSDOORVIDEO`. Lleva `omitempty`: un evento replicado de una boltdb
  anterior a este campo no trae uid y entonces la clave no aparece.
- **`event_id` es el ÚNICO campo agregado a este tópico desde 1.0.32**, verificado comparando las
  etiquetas `json:` contra la versión anterior. Lo nuevo que sí hay son dos tipos de mensaje
  completos, `CAMERATIME` y `COUNTERSDOORVIDEO`, y los dos van a **otro tópico**
  (`EVENTS/counterevents`). Un consumidor de `EVENTS/backcounter` no los ve.
- **`-withoutEventID` omite la clave** y devuelve el mensaje al formato previo a 1.0.32, byte por
  byte. Es una compuerta de compatibilidad temporal para una plataforma que rechaza el mensaje al
  ver el campo nuevo. Mientras esté activa **no se puede cruzar el paso con su video**, porque esa
  es la llave; el `COUNTERSDOORVIDEO` se sigue publicando intacto, pero queda huérfano del lado de
  la plataforma. El binario deja WARN en cada arranque mientras el flag esté puesto.
- `state` = estado de la puerta conocido por `EventActor` (`0` si nunca llegó un `MsgDoor`).
- `coord` = trama `$GPRMC` cruda, o `""` si el GPS no respondió en 180 ms o está viejo (>30 s).

Cámara caída (`type: "CounterDisconnected"`), tras fallar el keep-alive HTTP:

```json
{"timestamp":1723406400.123,"type":"CounterDisconnected",
 "value":{"coord":"$GPRMC,...","id":0,"type":"CAMARA"}}
```

Ojo: aquí `type` interno es `"CAMARA"` (español) mientras en los pasos es `"CAMERA"`, y el `id`
va fijo en `0` aunque la cámara vigilada por `PingActor` sea la de `192.168.188.21` (que es
`id 1`). Ambas cosas son inconsistencias reales del contrato ya desplegado — **no las
"corrijas" sin coordinar con quien consume**, romperías la integración en producción.

Dato relevante: **hasta 1.0.30 este mensaje nunca se publicaba**. Un panic por `ctx.Parent()` nulo
reiniciaba el actor antes de llegar al `Publish` (ver deuda conocida en CLAUDE.md), así que si un
consumidor nunca lo vio, no es que la cámara no fallara. Verificado publicándose en 1.0.30.

### `EVENTS/counterevents` — tamper y video

`type: "TAMPERING"`, misma forma que el paso, con `counters` marcando la puerta afectada:
`[1,0]` si `id == 0`, `[0,1]` si `id == 1`. Origen: eventos `scenechangedetection` y
`shelteralarm` de la cámara.

`type: "COUNTERSDOORVIDEO"`, cuando el clip de un paso **ya está escrito en disco**:

```json
{"timestamp":1787154668.359,"type":"COUNTERSDOORVIDEO",
 "value":{"id":0,"type":"CAMERA","event_id":"9668c0b7","gateway_hostname":"NE-RCX-0000",
          "metadata":"20260819T105047_NE-RCX-0000_p0_in_9668c0b7.json",
          "clip":"20260819T105047_NE-RCX-0000_p0_9668c0b7.mp4",
          "camera_serial":"DS-2XM6825G0/C-IVS20221126AAWRK97545100"}}
```

Tres cosas que hay que entender de este mensaje antes de tocarlo:

- **Llega tarde a propósito**, decenas de segundos después del `COUNTERSDOOR` correspondiente: hay
  que esperar a que la cámara grabe la ventana completa y después extraer, que va a tiempo real. Se
  publica acá y no junto al conteo porque en ese momento no se sabe el nombre final del archivo ni
  si la extracción va a tener éxito. Anunciarlo antes dejaría referencias colgadas.
- **Si no llega, ese paso no tiene video.** Es la única señal: no hay reintento infinito ni
  mensaje de error por paso. Los motivos (sin cobertura de grabación, disco lleno, cámara caída)
  quedan en el log del gateway.
- **Va uno por evento aunque varios compartan el `clip`.** En una ráfaga, `metadata` cambia en cada
  mensaje y `clip` se repite. Es deliberado: permite un cruce 1 a 1 por `event_id` sin desarmar
  listas.

`type: "CAMERAPOWER"`, del `CountingActor`, cuando se le corta o se le devuelve la energía a la
cámara por la GPIO de `-cameraPowerGPIO`. Una publicación **por transición**, nunca por ciclo:

```json
{"timestamp":1793335980.405,"type":"CAMERAPOWER",
 "value":{"type":"CAMERA","status":"poweroff","gpio":164,"level":1,"source":"ignition"}}
```

- `status` es `poweroff` (se cortó la energía porque `go-ignition` avisó que el vehículo se
  apagó), `poweron` (volvió la energía y la cámara ya tuvo su `-cameraBootDelay` para arrancar)
  o `power_error` (la escritura a sysfs falló; la línea **no** está en el nivel que dice la
  configuración y hay que mirar el equipo).
- **Este mensaje es lo que distingue "apagada a propósito" de "averiada".** Sin él, un equipo con
  la cámara sin energía por el apagado nocturno se ve exactamente igual que uno con la cámara
  rota: los dos dejan de contar y los dos se quedan callados. Si la plataforma solo consume un
  tipo de este tópico, que sea este.
- `gpio` y `level` son la línea y el nivel que significa apagado, para poder auditar la
  configuración del equipo desde la plataforma sin entrar por SSH.
- **Mientras `status` sea `poweroff` no hay que esperar `CAMERATIME` ni `COUNTERSDOORVIDEO`** de
  esa cámara, y la ausencia de conteo es la esperada. El binario además deja de consultarla, así
  que tampoco va a publicar `CounterDisconnected`.

`type: "CAMERATIME"`, del `CameraActor`, cuando corrige la configuración de hora de una cámara o
cuando detecta que su reloj derivó:

```json
{"timestamp":1787258987.460,"type":"CAMERATIME",
 "value":{"id":0,"type":"CAMERA","status":"reboot","camera":"192.168.186.91",
          "camera_serial":"DS-2XM6825G0/C-IVS20221126AAWRK97545100","drift_s":-0.7,
          "time_mode":"NTP","ntp_server":"ntp2.inm.gov.co","ntp_interval_min":60,
          "fixed":["codificación: SmartCodec -> false"],
          "storage":"ok","storage_free_mb":2304,
          "video_dir":"/SD/video","video_dir_free_mb":6842,
          "video_codec":"H.264","video_fps":20,"smart_codec":true}}
```

- `status` es `fixed` (se corrigió algo), `drift` (el reloj derivó más de `-timeDriftMax` durante
  tres ciclos), `ok` (volvió a hora después de haber alarmado), `storage` / `storage_ok` (el medio
  de grabación dejó de servir o se recuperó), `encoder_rejected` (un valor del perfil se escribió
  con OK y la cámara no lo guardó — el flag pide algo que el modelo no admite), `videodir` /
  `videodir_ok` (el disco del GATEWAY donde se dejan los clips cruzó el piso de `-videoMinFree`, o
  se recuperó) o `reboot` (se está reiniciando la cámara para aplicar el perfil de codificación).
- **`reboot` y `fixed` no salen los dos por el mismo cambio.** Cuando hay reinicio se publica solo
  `reboot`, que lleva el mismo arreglo en `fixed` y además avisa que la cámara se va a caer un
  momento. Dos mensajes para un cambio obligarían al consumidor a deduplicar.
- **`video_codec`, `video_fps` y `smart_codec` son lo OBSERVADO al revisar, antes de corregir.**
  En el ejemplo `smart_codec: true` con `fixed: [... -> false]` significa "estaba en true, lo
  dejamos en false"; el valor efectivo es el de `fixed`. Sirven para ver de lejos con qué está
  grabando cada cámara: el extractor maneja H.264 y H.265, así que ningún codec "rompe" nada, pero
  `smart_codec: true` sí anticipa clips congelados.
- `storage` y `storage_free_mb` (en MB) aparecen cuando se pudo consultar el medio. `storage`
  distinto de `ok` es la condición que hace imposible extraer **cualquier** clip.
- **`storage` es texto libre, no un enum cerrado.** Trae lo que diga la cámara (`ok`,
  `unformatted`, `error`, `idle`) más un valor propio del binario: **`nooverwrite`**, cuando la
  sobrescritura cíclica está apagada y por lo tanto la cámara va a dejar de grabar al llenarse.
  Se agregó como valor y no como clave nueva justamente para no cambiar la forma del mensaje.
- **No hay alarma por espacio libre bajo**, y es deliberado: con la sobrescritura encendida
  —el default— el espacio libre tendiendo a cero es lo normal en una cámara sana, y alarmar por
  eso daría un falso positivo en toda la flota. Lo que se vigila es la configuración.
- **`storage_free_mb` desaparece cuando vale 0**, por el `omitempty`. Su ausencia no significa
  "no se midió"; para eso está `storage`, que sí viaja siempre que se pudo consultar.
- **`video_dir_free_mb` no es `storage_free_mb`.** `storage_free_mb` es la SD **de la cámara**, que
  es de donde se lee la grabación; `video_dir_free_mb` es el disco **del gateway** (`/SD`), que es
  donde se escribe el clip. Dos discos distintos, dos fallas distintas: la SD de la cámara llena
  significa que no hay qué extraer, el disco del gateway lleno significa que no hay dónde ponerlo.
- **`video_dir_free_mb` viaja en TODOS los `CAMERATIME`**, no solo cuando el disco falla, y esa es
  la diferencia que lo hace útil: cuando el piso se cruza ya es tarde, y solo con la serie completa
  se ve venir la tendencia con semanas de anticipación. Aparece si hay `-videoDir` configurado y se
  pudo medir. Al cruzar el piso se publica además un `videodir` propio, con el mismo patrón de
  `storage` — una publicación al cruzar, otra al recuperarse, ninguna en el medio.
- **Un `videodir` sale UNA vez por ciclo, no una por cámara.** El disco es del gateway, así que el
  evento viaja con el primer resultado sano del ciclo; el `id` que lleva es el de esa cámara y no
  significa que el problema sea de ella.
- `ntp_reachable` aparece **solo cuando hubo deriva**: el test del servidor se consulta nada más
  en ese caso, para distinguir problema de red de problema de reloj. Su ausencia no significa
  que el servidor esté mal.
- **Se publica solo en los cambios de estado**, no en cada ciclo. Un mensaje por cámara cada
  media hora sería ruido que esconde lo que importa.
- `drift_s` es positivo si la cámara está adelantada respecto de la referencia (GPS, o el reloj
  del gateway si no hay trama válida).

## Suscribe

| Tópico | Parser | Efecto |
|---|---|---|
| `EVENTS/doors` | `parsePubSubDoorsEvents` | estado de puerta (`pubsub.Message` con `{coord,id,state}`) |
| `camera/doors` | `parseDoorsEvents` | estado de puerta (`api.DoorState` de `go-doors`) |
| `GPS` | `parseGPSEvents` | trama NMEA cruda; solo se guarda si empieza con `$GPRMC` |
| `EVENTS/ignition` | `parseIgnitionEvents` | estado del vehículo, de `go-ignition`; **solo con `-cameraPowerGPIO`** |
| `cameracountdiscovery/params` | `parseDiscoveryResponse` | dónde está `nats-gwIot`; **salvo con `-disablePlatformConfig`** |

Los dos formatos de puerta coexisten por compatibilidad: `EVENTS/doors` es el histórico,
`camera/doors` es el que responde `go-doors` tras el `DoorsStateRequest` publicado en `DOORS`.
Ambos terminan en el mismo `MsgDoor{ID, Value}`.

El estado de puerta **habilita o bloquea el conteo** (ver reglas de gating en CLAUDE.md): si
estos tópicos no llegan, la puerta se considera cerrada y los pasos se descartan con WARN.
Es el segundo motivo más común de "no cuenta" después del `Content-Type` del XML.

### `EVENTS/ignition` — apagado de la cámara

Lo publica **`go-ignition`**, que corre en el mismo gateway. Solo se escucha cuando se pasa
`-cameraPowerGPIO`; sin ese flag nadie se suscribe. Payloads reales capturados del equipo:

```json
{"value":{"state":1},"timestamp":1793335980.405281,"type":"SUSPEND_EVT"}
{"value":{"state":1},"timestamp":1793335980.401813,"type":"IgnitionEvent"}
```

| campo | |
|---|---|
| `value.state` | `0` NA, `1` UP (encendido), `2` DOWN (apagado) |
| `value.coord` | trama del GPS, opcional; se ignora |
| `timestamp` | **segundos con fracción**, no milisegundos |
| `type` | `SUSPEND_EVT` o `IgnitionEvent` |

**Solo se actúa sobre `SUSPEND_EVT`.** `go-ignition` lo emite junto a su `PowerEvent`
interno, o sea después de su propio periodo de gracia tras la llave: ya viene antirrebote.
El `IgnitionEvent` es el estado crudo y solo se registra.

`state: 0` (NA) se **descarta**: es "no sé", y tratarlo como apagado dejaría la cámara sin
energía por un dato que el propio `go-ignition` declara desconocido.

### `GWIOT/discovery` + `cameracountdiscovery/params` — dónde está nats-gwIot

Es el único intercambio donde este binario **publica una pregunta y espera respuesta**. Sirve
para encontrar al proceso `nats-gwIot`, que es quien baja la configuración de la plataforma
(ver `docs/contrato/configuracion-plataforma.md`).

```
go-hikvision  --> GWIOT/discovery               {"reply":"cameracountdiscovery/params"}
nats-gwIot    --> cameracountdiscovery/params   {"host":"127.0.0.1","port":8091}
```

Con ese `host:port` se activa un actor **remoto por protoactor** y se le pide un
`WatchKeyValue` del bucket de parámetros. Los datos NO viajan por MQTT; por acá va solo el
descubrimiento.

**El tópico de respuesta es propio de este binario.** `go-driverconsole` usa
`dconsolediscovery/params` y `go-counterpass` usa `counterpassdiscovery/params`. Si dos
procesos comparten uno, cada uno recibe las respuestas del otro y termina hablándole al
gateway equivocado.

El descubrimiento se reintenta solo cada 60 s mientras no haya respuesta. Que falle no
detiene nada: el binario cuenta igual con los flags, los defaults y el cache en disco.

## Notas de implementación

- El actor MQTT es un **singleton** por `sync.Once` (`getInstance`), spawneado en la raíz con
  nombre aleatorio. `Publish` es una función libre que no requiere contexto de actor.
- `Subscribe` es `SubscribeFun` con la firma que esperan los actores de descubrimiento de
  `go-gwiot`: el parser devuelve el mensaje **o un error como valor**, no como segundo
  retorno. Son dos convenciones que conviven en la flota; traducir en `pubsub.go` es más
  barato que cambiarle la firma a los cuatro parsers que ya existen.
- **Quien se suscriba fuera del paquete `client` tiene que llamar antes a `InitPubSub` con el
  sistema de actores correcto.** `getInstance(nil)` monta el singleton sobre un
  `actor.NewActorSystem()` propio si todavía no existe, y el binario queda con dos sistemas
  de actores y un cliente de MQTT huérfano, sin un solo error.
- `SubscribeFun` guarda la suscripción en el mapa **antes** de verificar conexión, así que las
  suscripciones se rehacen solas en `*actor.Started` tras un reinicio del actor.
- Si `SubscribeFun` falla porque el broker no está listo, los actores hijos hacen
  `logs.LogError.Panic` → reinicio supervisado. Es el comportamiento esperado al arrancar antes
  que mosquitto.
- Para inspeccionar en el equipo:
  `mosquitto_sub -h 127.0.0.1 -t 'COUNTERSMAPDOOR' -t 'EVENTS/#' -t 'DOORS' -v`
