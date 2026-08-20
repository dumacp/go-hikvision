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
| `EVENTS/counterevents` | `topicAddEvents` | por cada tamper, por cada clip escrito, y al corregir o detectar deriva en el reloj de una cámara | `TAMPERING`, `COUNTERSDOORVIDEO`, `CAMERATIME` |
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

`type: "CAMERATIME"`, del `CameraActor`, cuando corrige la configuración de hora de una cámara o
cuando detecta que su reloj derivó:

```json
{"timestamp":1787238867.212,"type":"CAMERATIME",
 "value":{"id":0,"type":"CAMERA","status":"fixed","camera":"192.168.186.91",
          "camera_serial":"DS-2XM6825G0/C-IVS20221126AAWRK97545100","drift_s":0,
          "time_mode":"NTP","ntp_server":"ntp2.inm.gov.co","ntp_interval_min":60,
          "fixed":["NTP ntp2.inm.gov.co:123 cada 1500 min -> ntp2.inm.gov.co:123 cada 60 min"]}}
```

- `status` es `fixed` (se corrigió algo), `drift` (el reloj derivó más de `-timeDriftMax` durante
  tres ciclos) o `ok` (volvió a hora después de haber alarmado).
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

Los dos formatos de puerta coexisten por compatibilidad: `EVENTS/doors` es el histórico,
`camera/doors` es el que responde `go-doors` tras el `DoorsStateRequest` publicado en `DOORS`.
Ambos terminan en el mismo `MsgDoor{ID, Value}`.

El estado de puerta **habilita o bloquea el conteo** (ver reglas de gating en CLAUDE.md): si
estos tópicos no llegan, la puerta se considera cerrada y los pasos se descartan con WARN.
Es el segundo motivo más común de "no cuenta" después del `Content-Type` del XML.

## Notas de implementación

- El actor MQTT es un **singleton** por `sync.Once` (`getInstance`), spawneado en la raíz con
  nombre aleatorio. `Publish` es una función libre que no requiere contexto de actor.
- `SubscribeFun` guarda la suscripción en el mapa **antes** de verificar conexión, así que las
  suscripciones se rehacen solas en `*actor.Started` tras un reinicio del actor.
- Si `SubscribeFun` falla porque el broker no está listo, los actores hijos hacen
  `logs.LogError.Panic` → reinicio supervisado. Es el comportamiento esperado al arrancar antes
  que mosquitto.
- Para inspeccionar en el equipo:
  `mosquitto_sub -h 127.0.0.1 -t 'COUNTERSMAPDOOR' -t 'EVENTS/#' -t 'DOORS' -v`
