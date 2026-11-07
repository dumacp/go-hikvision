# go-hikvision — contexto del proyecto

Binario Go embebido que integra cámaras **Hikvision de conteo de personas** (people counting)
con la plataforma de telemetría: recibe eventos de la cámara, los valida/acumula de forma
persistente y los publica por MQTT local. Corre en el gateway del vehículo (rutas `/SD/...`,
GPIO por sysfs, broker MQTT en `127.0.0.1:1883`).

## Comandos

```bash
go build ./...                    # compila todo
go vet ./...                      # limpio hoy
go test ./...                     # FALLA hoy (ver "Deuda conocida")
go run ./client/main -version     # imprime versión y sale con código 2

# ejecución local típica (dos puertas: id 0 frontal, id 1 trasera)
go run ./client/main -logStd -debug -socket :8088 -pathdb /tmp/countingdb \
  -zeroOpenState=false -zeroOpenState=true

# regenerar protobuf tras editar client/messages/msgcamera.proto
cd client/messages && ./protobuf.sh
```

**Dependencias**: `go-doors` y `go-actors` se consumen por versión publicada (`v1.2.1` y
`v0.1.0`); antes iban por `replace` al filesystem y ya no hace falta tenerlos clonados.

`go-params` se consume por la versión publicada de la rama `feat/cameracount-config`
(`v1.0.4-0.20261106182210-64e61f53dbce`), que es la que mapea `DEV_CAMERACOUNT_CONFIG`.
Cuando esa rama entre a la principal de `go-params` y tenga tag, conviene subir a esa versión.

**Queda UN `replace`, y es obligatorio**: `github.com/asynkron/protoactor-go => ../../asynkron/protoactor-go`.
El checkout local lleva dos parches que no existen en ningún commit publicado, y uno de ellos
**evita un panic en armv7**, que es la arquitectura de destino:

```go
// actor/process_registry.go — SliceMap.GetBucket
index = (int(hash) & 0x7FFFFFFF) % len(s.LocalPIDs)   // el parche
```

En ARM `int` es de 32 bits, así que un hash murmur32 mayor a 2³¹ se vuelve **negativo** al
convertirlo y `s.LocalPIDs[índice]` hace `index out of range`. Medido: **el 50% de los nombres**
cae en índice negativo, incluidos `counting/video`, `counting/events`, `counting/doors` y
`counting/ping`, que son los de este binario. En un equipo de 64 bits no se ve nunca. Upstream
sigue sin arreglarlo (verificado contra la rama `dev`) y no hay issue abierto. **Pero ya
existe el fork publicado que CLAUDE.md venía pidiendo**: `github.com/dumacp/protoactor-go`
lleva el parche (verificado en
`v0.0.0-20261101194920-b85f35a0f362`, `actor/process_registry.go:45`) y `go-params` ya lo
consume por `replace`. Migrar este repo a ese fork cambiaría el `replace` al filesystem por
uno a una versión publicada; **no se hizo acá** porque no es parte de este trabajo y hay que
verificarlo en el equipo, no solo compilarlo. El segundo parche
—el `if err != nil` de `remote/proto_serializer.go`— **sí** lo adoptaron upstream y ya no haría
falta con una versión nueva.

**No quites ese `replace`.** Quitarlo compila y pasa los tests en el equipo de desarrollo, y el
binario entra en panic al arrancar en el vehículo. La salida de fondo es publicar un fork con el
parche; mientras tanto, el clon local es la dependencia.

**Versión**: la constante `showVersion` en [client/main/main.go](client/main/main.go#L19) es la
única fuente de versión. **No la subas por tu cuenta.** Se sube cuando hay acuerdo de que lo que
está en `master` es lo que se va a desplegar, y el número lo decide quien lo despliega — un commit
que la sube por cada cambio produce versiones que nunca existieron como binario (en esta historia
`1.0.35` es una sintaxis que se revirtió en `1.0.36`). Commiteá el cambio funcional y dejá la
constante quieta.

## Arquitectura (protoactor-go)

Jerarquía de actores que se levanta en [client/main/main.go](client/main/main.go):

```
root
├── counting          CountingActor  ← persistence.Mixin (boltdb)
│   ├── events        EventActor     → arma JSON y lo devuelve al padre
│   ├── doors         DoorsActor     → estado de puertas vía MQTT
│   ├── ping          PingActor      → keep-alive HTTP a la cámara
│   ├── gps           GPSActor       → última trama $GPRMC
│   ├── video         VideoActor     → recorta el video de cada paso (solo con -videoDir)
│   ├── camera        CameraActor    → hora, NTP, grabación, SD y codec (-ntpServer o flag de codec)
│   └── ignition      IgnitionActor  → apagado de la cámara por GPIO (solo con -cameraPowerGPIO)
├── listenner         ListenActor    → servidor HTTP que recibe los eventos de la cámara
├── parametros        parametros.Actor → configuración de plataforma (salvo -disablePlatformConfig)
│   └── params-actor  go-params      → y debajo params-data-actor y discover-actor
└── pubsub-actor-XXXX singleton MQTT (lo crea InitPubSub desde counting/Started)
```

El paquete [video/](video/) extrae un tramo grabado y lo escribe como MP4, en Go puro, por dos
vías: la descarga HTTP del archivo grabado (`ExtraerDescarga`, con un lector de MPEG-PS propio)
y la reproducción por RTSP (`Extract`, con `gortsplib` + `mediacommon`). Sin ffmpeg **a propósito**: el binario cross-compila a cualquier
arquitectura con un solo `go build` y las credenciales nunca aparecen en la línea de comandos de
otro proceso. Costo medido: +1.8 MB en armv7.

El paquete [isapi/](isapi/) es el transporte hacia la cámara: HTTP + XML con autenticación Digest
(implementada a mano, la stdlib no la trae y no se agregó dependencia). Es deliberadamente tonto —
sin actores, sin política de logs y **sin reintentos**: quién llama, cada cuánto y qué hacer con un
error lo decide el actor, para que esas reglas vivan en un solo lugar. `ErrUnauthorized` es
**terminal**: la cámara bloquea el usuario tras varios fallos.

Flujo de datos:

```
cámara --HTTP POST XML--> peoplecounting.Listen (:8088)
   --> chan *peoplecounting.Event --> ListenActor (dedup temporal, delta enter/exit)
   --> messages.Event --> CountingActor (gating por puerta, persistencia, acumulados)
   --> EventActor (adjunta GPS + estado de puerta, serializa JSON)
   --> CountingActor --> Publish(topic) --> MQTT local
```

### Identificación de cámara/puerta (`id`)

`id` distingue puerta **frontal (0)** de **trasera (1)** y se deriva de la IP de origen en
`ListenActor.doorID` ([client/listen-actor.go](client/listen-actor.go)):

- con `-camera` configurado, el índice de la coincidencia es el `id`. Si ninguna coincide, cuenta
  como puerta 0 **con WARN** — se prefiere sumar el pasajero en la puerta equivocada antes que
  perderlo, pero queda registrado;
- sin `-camera`, la regla histórica: `RemoteAddr` con `192.168.188.21` → `id 1`, vacío → `id 1`,
  cualquier otra → `id 0`.

**El keep-alive consulta la primera cámara de `-camera`.** Sin `-camera` queda `192.168.188.21`,
que es la dirección **estándar** de la cámara en el despliegue normal — no un valor heredado, sino
el que de verdad tienen los equipos en campo. Antes era *siempre* esa, y en un equipo con otro
direccionamiento —un banco de pruebas, una flota con otro plan de IPs— el `GET` fallaba cada ciclo: medido en FLO-W7-0138, con
las cámaras en `.91` y `.93`, **dos `CounterDisconnected` falsos en cinco minutos** mientras las
dos cámaras contaban sin problema. Una alarma que miente es peor que no tenerla, porque entrena a
ignorar la que sí importa. Ojo con el ruteo: si los eventos llegan por NAT o port-forward, el
`RemoteAddr` es el del router y **todas las puertas colapsan en una** (verificado: con ruteo puro
la IP de origen se preserva).

### Dos generaciones de cámara, UN solo formato de evento

El binario atiende **DS-2XM6825G0** y **DS-2XM6825G1**, y desde que la G1 está en firmware
**V4.2.2** las dos mandan lo mismo: `eventType: PeopleCounting` con `statisticalMethods`, donde
`realTime` trae los **acumulados** del día. Un solo canal, un solo parser, una sola regla de
conteo, un solo `COUNTERSDOOR`.

Lo que todavía difiere es de transporte, y está absorbido:

| | G0 | G1 con V4.2.2 | cómo se resuelve |
|---|---|---|---|
| `Content-Type` | `application/xml` | **`multipart/form-data`** | `xmlDeMultipart` saca la parte XML |
| `statisticalMethods` | `timeRange` | **`TimeRange`** | `esConteoDeVentana` compara sin distinguir mayúsculas |
| namespace | `urn:psialliance-org` | `isapi.org/ver20` | el parser los ignora |
| sabotaje | `scenechangedetection` | `shelteralarm` | ramas separadas, mismo `TAMPERING` |
| reproducción por RTSP | sirve | **ROTA**: devuelve un stream roto, el mismo para cualquier hora | `ExtraerDescarga` por `/ISAPI/ContentMgmt/download` |
| descarga por HTTP | **no existe** (`methodNotAllowed`) | sirve, MPEG-PS con H.265 | se prueba primero y se aprende por puerta |
| búsqueda de grabaciones | UTC | **reloj de pared**, ida y vuelta | `HasCoverage` prueba las dos y se cachea por puerta |
| archivos e índice de grabación | sin medir | **archivos de 5 min**, índice **hasta ~2 min atrasado** | tramos unidos + reintento hasta 3 min |

**La grafía de `statisticalMethods` no es cosmética.** Medido el mismo día en el mismo gateway:
`192.168.186.91` escribió `timeRange` 16 veces y `192.168.186.93` escribió `TimeRange` 21 veces.
El filtro comparaba distinguiendo mayúsculas y solo atrapaba la primera. Colarse **corrompe el
acumulado en cascada**: el evento de ventana trae `enter=1` mientras el real va en 21,
`CountingActor` calcula −20, entra por la rama negativa, suma 1 porque es menor que 4 y deja
`rawXmap` en 1 — con lo que el siguiente evento real da un salto de 20 y también se descarta.

**El formato de la G1 con V3.2.2 se soportó y se QUITÓ.** Esa versión mandaba
`eventType: counting` con el conteo de una ventana de tiempo en vez del acumulado. Nunca podía
tener video —no dice en qué instante cruzó cada persona— y mantener vivo un camino que no se
puede probar solo confundía el diagnóstico. Hoy el parser lo **rechaza con un error que nombra el
firmware y dice qué hacer**:

```
eventType "counting": la camara esta en firmware V3.2.2 y reporta por ventana, no por
cruce. Actualizala a V4.2.2 o posterior
```

**Se quitó entero, sin dejar excepciones.** Ese camino traía además un campo `delta` en el
`.proto` para marcar que `value` era un incremento y no el acumulado, y con él una rama aparte en
la aritmética de `CountingActor`. Las dos cosas se eliminaron: `client/counting-actor.go` volvió a
ser **idéntico** al de antes de todo esto salvo por `pingAddr`, y hoy hay **una sola** regla de
conteo. El campo nunca llegó a publicarse —vivió solo en los commits de este trabajo—, pero el
número **6 quedó `reserved`**: las boltdb de las pruebas del 2026-10-30 tienen eventos con esa
etiqueta y se replican al arrancar, así que darle otro tipo al número los haría leer mal.

**Antes de correr el binario nuevo en un equipo que probó el formato de ventana, borra su
`/SD/boltdbs/countingdb`.** Hoy eso es solo el banco FLO-W7-0138: ningún equipo en campo corrió
nunca un binario con el campo. Medido en las copias guardadas: la base de pruebas tenía 4 eventos
con `delta`, y la copia del gateway ninguno.

**Un `eventType` desconocido da error con su nombre y la IP de origen.** Antes el `switch` sin
rama `default` devolvía `(nil, nil)`, el handler mandaba un `Event{Data: nil}` al canal, el type
switch de `ListenActor` tampoco tenía `default`, y la cámara recibía **HTTP 200**: tres capas de
silencio. Así se perdió el conteo de la G1 hasta que hubo que capturar los POST en crudo.

**El parser ya no exige la declaración `<?xml ?>`.** Antes consumía el primer token a ciegas
asumiendo que estaba; un cuerpo sin ella perdía su elemento raíz y fallaba con un mensaje que
nombraba el segundo elemento, mandando a buscar el problema al lugar equivocado.

### El evento puede venir en multipart

**Desde el firmware V4.2.2 la G1 adjunta la foto del cruce**, y con eso cambia el `Content-Type`
del POST:

| firmware | `Content-Type` | |
|---|---|---|
| G0 (cualquiera) y G1 hasta V3.2.2 | `application/xml; charset=UTF-8` | el camino de siempre |
| G1 desde **V4.2.2** | `multipart/form-data; charset="UTF-8"; boundary=…` | una parte XML + la imagen |

`xmlDeMultipart` saca la parte XML y se la da al mismo parser; **el camino del XML pelado no
cambia**, que es el de todas las cámaras en campo. Hay una prueba de extremo a extremo
(`TestListenAceptaLasDosFormas`) que mete las dos formas por el servidor real y falla si alguien
rompe una.

**La parte XML se elige por su `Content-Type` y, si no lo trae, porque el contenido empieza por
`<`** — no por un nombre de parte fijo, que el próximo firmware cambiaría. **La imagen se descarta
sin leerla**: hoy nada la consume y serían cientos de kB por evento cargados en memoria para
tirarlos. Cuando haga falta, `xmlDeMultipart` es el único lugar que hay que tocar.

**El parser ya no exige la declaración `<?xml ?>`.** Antes consumía el primer token a ciegas
asumiendo que estaba; un cuerpo sin ella perdía su elemento raíz y fallaba con un mensaje que
nombraba el segundo elemento, mandando a buscar el problema al lugar equivocado.

### Reglas de conteo (CountingActor)

Sobre `messages.Event_INPUT` / `_OUTPUT` en [client/counting-actor.go:245-323](client/counting-actor.go#L245-L323):

- La cámara envía **acumulados**; el delta es `msg.Value - rawXmap[id]`.
- Solo se acepta `0 < diff < 10`. `diff >= 10` se descarta (log WARN). `diff < 0` (reinicio del
  contador de la cámara) solo se acepta si `msg.Value < 4`.
- **Gating por puerta**: si la puerta está cerrada el conteo se descarta, salvo que
  `-countWithCloseDoor` esté activo para ese `id`. El estado "abierto" es `1` por defecto y `0`
  cuando se pasa `-zeroOpenState=true` para ese `id`.
- `inputsmap`/`outputsmap` son los contadores publicados; `allInputsmap`/`allOutputsmap`
  acumulan también lo descartado por gating; `rawXmap` guarda el último acumulado de la cámara.
- Durante `Recovering()` se recalcula el estado sin reenviar eventos ni republicar.

### Flags (`client/main/flag.go`)

Los tres flags por puerta —`-camera`, `-zeroOpenState`, `-countWithCloseDoor`— son **repetibles**,
pero **no siguen la misma regla**, y la diferencia es deliberada.

#### Los dos switches: uno cubre todas las puertas

```bash
-zeroOpenState=true                          # aplica a TODAS las puertas
-zeroOpenState=false -zeroOpenState=true     # posicional: puerta 0 y puerta 1
```

Sintaxis sin cambios: un booleano por aparición, y cualquier valor distinto de `TRUE`
(case-insensitive) es `false` **sin avisar** — hay equipos en campo dependiendo de eso desde 1.0.25.

Lo que cambió es a qué puertas aplica **una sola** aparición: antes solo a la 0, ahora a todas
(`applyDoorBool` en [client/main/flag.go](client/main/flag.go)). Estos switches describen cómo está
cableado el vehículo —si el estado "abierta" llega como `0` o como `1`— y eso no cambia entre la
puerta delantera y la trasera, así que un valor suelto es la convención del vehículo entero. Dos o
más apariciones siguen siendo posicionales, que es la forma de darle un valor distinto a cada puerta.

**Por qué importaba.** `-zeroOpenState` nació en **1.0.9** como un `flag.BoolVar` global, cuando
`CountingActor` modelaba una sola puerta con escalares (`openState int`, `inputs int64`). En
**1.0.25** los escalares pasaron a `map[int]...` y el flag se volvió posicional, con lo cual un flag
suelto pasó de significar "la única puerta" a "solo la puerta 0". Un equipo con solo la cámara
trasera y un único `-zeroOpenState=true` quedaba configurando la puerta 0 mientras los eventos caían
en la 1, que corría con el default — y **no daba error**, porque el gating
([counting-actor.go:316-323](client/counting-actor.go#L316-L323)) usa `openState = 1` cuando el `id`
no está en el mapa. Dos síntomas, los dos visibles en el log como
`counting inputs when door (id: 1) is closed`:

- si el sistema de puertas publica "abierta" como `0`, la puerta queda invertida y **descarta todos
  los cruces reales**;
- si nunca llega un `MsgDoor` con ese `id`, el `!ok` de esa condición **descarta todo** el conteo de
  esa puerta salvo que `-countWithCloseDoor` esté activo para ella.

**Ojo al actualizar** un equipo de dos puertas que hoy pasa un único valor: la puerta 1 pasa del
default a ese valor. Si el default era el correcto por casualidad, esto la cambia.

#### `-camera`: el `id` va escrito

Acá sí hay dos formas, porque una dirección IP no se puede replicar a todas las puertas:

```bash
-camera 192.168.188.21:1                     # explícita: el id va escrito
-camera 10.0.0.1:0 -camera 10.0.0.2:1        # dos puertas, el orden no importa
-camera 10.0.0.1 -camera 10.0.0.2            # posicional, la histórica
```

**La forma posicional es una trampa en el despliegue más común**: un vehículo con solo la cámara
trasera. Escribir un único `-camera 192.168.188.21` la deja como puerta **0**, mientras la regla
histórica clasificaba esa misma dirección como puerta **1** — los contadores se moverían de
`inputs1` a `inputs0` y la plataforma vería una serie congelarse y otra arrancar de cero, sin
ningún error. Con `:1` eso no pasa.

`resolveCameras` resuelve las apariciones a un slice indexado por puerta, y **falla al arrancar** en
vez de adivinar. Detenerse es más barato que contar la puerta equivocada durante un turno: los
contadores publicados quedan mal y no hay forma de repararlos después.

- **mezclar las dos formas es error.** Una aparición posicional al lado de una explícita no tiene un
  significado obvio —¿toma el siguiente hueco libre, o la siguiente posición?— y cualquier respuesta
  sería una regla más que recordar;
- **el límite de 15 en el `id` existe para atrapar un puerto TCP escrito como id.** Sin él,
  `-camera ip:8080` armaría un slice de 8081 entradas y no contaría nada. Y un puerto en la
  dirección **no se admite** de todos modos: el HTTP de ISAPI lo respetaría, pero la extracción usa
  RTSP en 554 y quedaría mal armada;
- **repetir un `id` es error**: una cámara taparía a la otra en silencio;
- los `id` que nadie reclama quedan vacíos, y todo el binario ya los salta (`doorID` no los compara,
  `CameraActor` no los revisa, `VideoActor` no extrae de ellos).

Sin `-camera` se mantiene **exactamente** la regla histórica, para no alterar los equipos ya
desplegados. Si un equipo solo tiene la trasera en `192.168.188.21` y ya está en producción, no
pasar `-camera` es lo más seguro — salvo que se quiera video o mantenimiento de cámara, que sí lo
exigen.

### Extracción de video (`-videoDir`)

Deshabilitada salvo que se pase `-videoDir`, y además exige `-camera` y credenciales: sin las tres
cosas el actor no se crea y el conteo sigue igual. Flags: `-videoPreRoll` (10s), `-videoDuration`
(16s), `-videoQueue` (500), `-videoMinFree` (512 MiB).

Los defaults de 10 y 16 segundos están medidos, no elegidos: el `dateTime` del evento llega entre
**2 y 7 segundos después del cruce físico**, y el retraso varía. Con 5 s de pre-roll el cruce
quedaba en el borde del clip o afuera; con 10 s, tres personas cruzando en fila entraron en los
segundos 7 a 11 de la ventana. No los bajes sin volver a medir contra tráfico real.

Salida, con nombres autosuficientes y un sidecar por evento para quien después suba los archivos:

```
/SD/video/2026-08-19/20260819T105047_NE-RCX-0000_p0_9668c0b7.mp4        ← el clip
/SD/video/2026-08-19/20260819T105047_NE-RCX-0000_p0_in_9668c0b7.json    ← entrada, offset_s 10
/SD/video/2026-08-19/20260819T105048_NE-RCX-0000_p0_out_29e08cd5.json   ← salida, mismo clip
```

El nombre lleva fecha y hora locales, el **hostname del gateway**, la puerta, el tipo (solo en el
sidecar) y el `uid` del evento. Es autosuficiente a propósito: en el repositorio final la
estructura de directorios se pierde, y así se puede buscar por fecha, por equipo, por puerta o por
`event_id` sobre un directorio plano.

**Los eventos de una ráfaga comparten el clip que los contiene.** El `.mp4` no lleva sufijo
`in`/`out` porque es de una puerta y una ventana, y puede contener entradas y salidas; el tipo
pertenece al evento, o sea al sidecar. Cada sidecar dice en qué segundo del video ocurrió su cruce
(`offset_s`), así que el consumidor no tiene que buscarlo.

Sin agrupar, una ráfaga de 10 pasos a un evento por segundo producía **10 clips y 5.5 MB, la mitad
de ellos byte a byte idénticos**: la cámara ajusta el `starttime` al keyframe anterior, con lo que
dos eventos del mismo GOP (2.5 s) devuelven los mismos bytes. Agrupando quedan **3 clips y 1.4 MB**,
y la cola drena en 49 s en vez de 112.

La agrupación decide **después** de esperar a que la ventana esté grabada, no al recibir el evento:
si agrupara al recibirlo, el primer grupo sería de uno solo porque los pasos siguientes aún no
llegaron, y volverían los duplicados. Se absorben los pendientes de la misma puerta cuyo evento
caiga en la ventana con al menos `videoMinPostRoll` de video después, para que ningún cruce quede
cortado en el borde.

El clip y cada sidecar se escriben como `.part` y se **renombran al terminar**: el rename es atómico, así que el
proceso que recorra el directorio nunca ve un archivo a medio escribir y no hace falta coordinar.
Este binario **no borra clips** —de eso se encarga quien los suba— pero **deja de extraer** cuando
el espacio libre baja del piso de `-videoMinFree` (512 MiB por defecto, `0` lo deshabilita).

**Ante el disco lleno aborta, no sobrescribe.** No hay rotación ni borrado: los nombres llevan el
`uid` del evento, así que no existe un "más viejo" al que apuntar, y borrarlo sería borrar el que
lleva más tiempo esperando que lo suban — este binario no sabe cuáles ya se subieron. Se pierde el
video del paso en curso, sin reintento; el conteo sigue igual. El aviso va **una vez por episodio**
(`sinEspacioAvisado`) y se rearma al recuperarse: con el disco al tope fallan todos los pasos, y un
ERROR por evento serían cientos de líneas diarias iguales. Si `Statfs` falla se sigue extrayendo
—fail-open a propósito— pero ahora deja WARN una vez; antes era silencioso y no se podía auditar.

`CameraActor` mide ese mismo disco una vez por ciclo y lo publica como `video_dir_free_mb` en
**todos** los `CAMERATIME`, más un evento propio al cruzar el piso (`videodir` / `videodir_ok`),
con el mismo patrón que `storage`/`storage_ok`. Va ahí y no en `VideoActor` porque este solo
despierta cuando pasa un pasajero, y el disco hay que verlo venir aunque no pase nadie. El dato es
del gateway, así que sale **una vez por ciclo**, con el primer resultado sano, no una vez por
cámara.

**La reproducción por RTSP de la G1 (V4.2.2) está ROTA, y durante una semana se la dio por
buena.** Medido el 2026-10-30 contra la DS-2XM6825G1 a las 17:53 locales, UTC−5:

```
starttime=20261030T225358Z   (UTC)              -> 500 Internal Server Error
starttime=20261030T175358Z   (reloj de pared)   -> 161 KB, 194 fotos, hueco 0.10 s
```

De ahí se concluyó que la cámara lee el `starttime` como su hora local y se agregó
`Request.HoraLocal` (`Extract` intenta UTC y reintenta en hora local; `Result.HoraLocal` dice
cuál funcionó). **La segunda línea no era un clip válido, y nadie miró la imagen.** Medido el
2026-11-07, decodificando cuadros con `ffmpeg`:

| cómo se pide | qué devuelve |
|---|---|
| en vivo, `/Streaming/channels/101` | perfecto |
| grabado, RTSP, **siete** `starttime` distintos entre 09:26:25 y 09:26:55 | **el mismo clip byte por byte**: solo la franja superior se decodifica, el resto verde, y el OSD marca `2026-11-06` |
| grabado, RTSP, con el `playbackURI` exacto de la búsqueda (con `name` y `size`) | el mismo stream roto |
| grabado, RTSP, de ayer | igual de roto |
| grabado, `POST /ISAPI/ContentMgmt/download` con el `playbackURI` de la búsqueda | **perfecto**: OSD `2026-11-07 09:26:23`, MPEG-PS con H.265 |
| grabado, `download` con un `playbackURI` de 16 s sin `name`/`size` | se cuelga sin entregar nada |

Con `ffmpeg` el stream de RTSP sale idéntico, así que no es el extractor: **la grabación está
bien y la reproducción por RTSP de este firmware no**. (Un primer análisis lo atribuyó a que el
RTSP traía ~25 KB/s contra ~247 KB/s de la SD: era falso. En el archivo de la SD el video pesa
~20 KB/s y el resto son datos privados de Hikvision en el stream `0xBD`, que el binario
descarta.) `HoraLocal` sigue en el código porque no estorba —en la G0 el primer intento
en UTC funciona—, pero su premisa sobre el RTSP de la G1 no está demostrada. Lo que sí está
medido es la convención de la **búsqueda** (abajo). La G1 se extrae por la descarga HTTP.

**Y la verificación del clip tiene que mirar la imagen.** `max_gap_s` y `fps_effective` salieron
normales en los clips rotos (191 muestras, hueco 0.23 s): miden la cadencia de las muestras, no
si se pueden decodificar. Un clip "exitoso" no está verificado hasta que alguien vio un cuadro.

**La G1 se extrae por descarga HTTP**, y `VideoActor` la intenta **primero en toda cámara**: si
responde que no la soporta (`isapi.ErrDescargaNoSoportada`, lo que da la G0) la puerta queda en
RTSP hasta el próximo arranque, sin configuración. El orden importa: el RTSP de la G1 no da
error, entrega un clip roto, así que no hay forma de detectarlo y caer a la descarga.

Cómo funciona `video.ExtraerDescarga`, y por qué así:

- **La cámara solo entrega archivos enteros.** `Download` exige el `playbackURI` de la búsqueda
  con `name` y `size`; uno recortado a la ventana se cuelga. Por eso `Cobertura.Archivos` dice
  qué archivos cubren la ventana —uno, o dos si cruza un corte— y se bajan en orden.
- **Se lee en streaming y no se guarda nada intermedio.** Lo anterior a la ventana se descarta al
  llegar, salvo el GOP que la abre; al pasar el final se **cierra la conexión**. En disco queda
  solo el MP4 (`.part` + rename) y sus sidecars, igual que por RTSP. El costo es de red: en el
  peor caso —ventana al final de un archivo de 5 min— se leen ~74 MB por el cable a la cámara,
  casi todo datos privados `0xBD`. Medido en el banco: **5 s por clip**, contra ~16 s del RTSP.
  En el gateway (armv7) está sin medir.
- **El instante de cada cuadro es el `Desde` del archivo más el avance de su PTS**, verificado
  contra el OSD: el cuadro que el binario ubica en 09:26:34 muestra `09:26:34`.
- **El empalme entre archivos se corrige.** El `Desde` viene redondeado al segundo y el IDR que
  abre el segundo archivo caía *antes* que el último cuadro del primero; el extractor de DTS lo
  rechazaba y el resto del GOP salía gris. Medido en el corte de las 09:28:38. Ahora cada
  archivo arranca detrás del último cuadro visto, y un cuadro sin DTS válido **hace fallar la
  extracción** en vez de escribir un clip roto.
- **`Result.Inicio` es el primer frame real**, y con él `offset_s` sale exacto (ver el sidecar).

Verificado de punta a punta el 2026-11-07, **mirando la imagen**: tres cruces, dos clips (uno
agrupa dos eventos), el cruce visible en el segundo esperado y `ffmpeg` sin un solo error de
decodificación. La muestra real de la prueba está en `video/testdata/g1_v422_gop.ps` (94 KB,
4.6 s, sin los `0xBD`).

**La búsqueda de grabaciones tiene el mismo problema, y por correr ANTES tapaba el arreglo
anterior.** `POST /ISAPI/ContentMgmt/search` en la G1 lee los instantes de la petición como su
reloj de pared y **responde** los tramos igual, todos con `Z`. Medido el 2026-11-07, misma
ventana de hace 5 a 2 minutos:

```
pedida en UTC         13:59:40Z..14:02:40Z  -> NO MATCHES
pedida en hora local  08:59:40Z..09:02:40Z  -> OK, tramo 08:58:59Z..09:03:59Z
```

Como `HasCoverage` es la guarda previa, cada evento de la G1 terminaba en `sin grabación` y
el reintento en hora local de `Extract` nunca llegaba a ejecutarse. Ahora `HasCoverage` prueba
UTC primero y la hora local después —con lo que las cámaras en campo no cambian—, reinterpreta
en esa misma convención los tramos que devuelve, e informa en `Cobertura.HoraLocal` cuál
funcionó. `VideoActor` la guarda por puerta (`horaLocal`) y la pasa a la búsqueda **y** al
recorte, así que desde el segundo pasajero no hay intentos fallidos. Un desfase explícito
(`-05:00`) nunca se reinterpreta: solo la `Z`, que es la que miente. La documentación oficial
dice lo contrario —`Z` es UTC— y no tiene ningún campo para elegir la convención: es
comportamiento del firmware, por eso se detecta.

**Y la G1 tiene además dos rarezas del índice, sin ajuste posible.** Revisado: ni el track,
ni `Storage/extension` (cuyas capacidades no declaran el `packDuration` de la guía RaCM), ni
los PDF de `docs/` exponen nada que las controle.

- **Graba en archivos de 5 minutos** y la búsqueda devuelve un tramo por archivo, pegados
  (`09:11:21..09:16:21`, `09:16:21..`). Exigir que UN tramo cubra la ventana rechazaba toda
  ventana que cruzara un corte — uno de cada ~19 pasos. `unirTramos` une los contiguos con un
  hueco de hasta 2 s.
- **El índice va hasta ~2 minutos atrasado y avanza de a saltos** (consultado cada 15 s: a las
  09:16:10 terminaba en 09:14:25, y después quedó más de un minuto clavado en 09:16:55). Con
  la espera fija de `videoSettleDelay` la ventana casi nunca figuraba grabada. La búsqueda mira
  ahora `BusquedaAtras` (3 min) antes de la ventana: si la cámara viene grabando desde antes y el
  índice no llega al final, devuelve `Pendiente` en vez de "no hay", y el grupo vuelve a la cola
  cada `videoIndiceReintento` (20 s) hasta `videoIndiceEspera` (3 min, con el reloj del
  gateway). Verificado contra la cámara: un cruce encontró su tramo al primer reintento, el
  otro a la primera. Los clips de esa prueba salieron rotos por el RTSP (arriba); con la
  descarga HTTP, el mismo reintento termina en clips correctos.

**El costo del reintento**: mientras un grupo espera su turno, la cola de video espera con él,
así que en un vehículo con dos cámaras el clip de la otra puede salir hasta unos 2 minutos
tarde. El conteo no se toca. Pasado el plazo, el WARN dice hasta dónde llegaba el índice, para
no confundirlo con una cámara que no graba.

**Y la lección que costó cara: un `500` significa "no hay grabación EN ESE INSTANTE", no "esta
cámara no graba".** Leerlo como lo segundo llevó a cambiar una microSD, formatear cuatro veces,
reiniciar cinco, hacer un reset de fábrica —que borró la calibración de conteo y dejó la puerta
sin contar— y actualizar el firmware, todo persiguiendo un fallo que estaba en el parámetro. La
señal que lo delataba estuvo disponible desde el principio: **el stream en vivo funcionaba
perfecto mientras la reproducción fallaba.** Cuando un equipo responde bien por un camino y mal
por otro, el sospechoso es cómo se le pregunta, no el hardware. **Y la segunda mitad de la
lección, del 2026-11-07:** un `200` con bytes tampoco prueba nada — el "arreglo" de ese día
devolvía un stream roto, y se lo dio por bueno durante una semana porque nadie abrió el clip.

Cuatro cosas medidas contra una cámara real que explican el diseño:

- **Pedir un instante sin grabación no da error: devuelve el tramo disponible más cercano.** Por
  eso `isapi.HasCoverage` es una guarda obligatoria antes de cada extracción. Sin ella un clip
  queda con el nombre de un evento y muestra otro momento.
- **La ventana termina después del evento**, así que al recibirlo esa parte todavía no está
  grabada. El actor espera `videoSettleDelay` tras el final de la ventana antes de pedirla; sin esa
  espera la verificación de cobertura falla siempre. Medido en la G0; en la G1 el índice va más
  atrasado y hace falta el reintento descrito arriba.
- **La reproducción va a tiempo real**: un recorte de 10 s tarda 10 s. Se extrae de a uno con cola
  acotada, y por eso el trabajo va en goroutine y nunca dentro de `Receive`.
- **Un salto del reloj de la cámara invalida los pendientes** de esa puerta: su marca de tiempo ya
  no apunta a la misma posición de la grabación. `ListenActor` avisa con `MsgClockStep` y el actor
  los descarta. Es el mismo mecanismo que detecta el `clock stepped back`.

La hora del evento viaja en `messages.Event.Timestamp` (segundos Unix, campo 4 del `.proto`). Es el
**reloj de la cámara**, que es el mismo que indexa sus grabaciones: por eso el recorte queda
alineado aunque ese reloj esté corrido respecto de la hora real. Y el nombre del archivo hereda ese
reloj: si está corrido, el nombre lo refleja — coherente con el video, pero no con la hora real.

### El sidecar del clip

Es el contrato con el proceso que sube los archivos, que no vive en este repo:

```json
{
  "event_id": "9668c0b7",
  "gateway_hostname": "NE-RCX-0000",
  "door": 0,
  "type": "in",
  "event_time": "2026-08-19T10:50:47-05:00",
  "event_time_ms": 1787154647000,
  "clip": "20260819T105047_NE-RCX-0000_p0_9668c0b7.mp4",
  "clip_start_requested": "2026-08-19T10:50:39-05:00",
  "offset_s": 8,
  "duration_s": 12,
  "camera": "192.168.186.91",
  "camera_serial": "DS-2XM6825G0/C-IVS20221126AAWRK97545100",
  "camera_mac": "bc:9b:5e:e7:ef:05",
  "camera_counter": 1,
  "samples": 201,
  "bytes": 1264740,
  "max_gap_s": 0.08,
  "fps_effective": 20.08
}
```

Cuatro precisiones que evitan malinterpretarlo:

- **`event_time_ms` va en milisegundos pero la resolución de origen es de un segundo**: la cámara
  envía el `dateTime` sin fracción, así que siempre termina en `000`. No leas precisión que no hay.
- **`clip_start_requested` es el instante PEDIDO, no el primer frame.** La cámara posiciona la
  reproducción en el keyframe anterior, así que el video puede empezar hasta un GOP antes (medido:
  2 s a 20 fps en la G0, 4.2 s en la G1). Da más pre-roll que el configurado, nunca menos.
  **`offset_s` en cambio se mide contra el primer frame real cuando se conoce** —siempre en
  los clips bajados por descarga (`video.Result.Inicio`)— y ahí es exacto. Por RTSP la cámara
  no dice dónde empezó, se mide contra el instante pedido y se queda corto hasta un GOP.
- **`camera_serial` y `camera_mac` se consultan una sola vez por cámara** con
  `isapi.GetDeviceInfo` y quedan en caché. Si esa consulta falla los campos van vacíos pero **la
  extracción sigue**: el clip vale más que su metadato. Son la identidad estable del equipo,
  mientras `camera` es la IP y cambia con el direccionamiento.
- **`camera_counter` es el incremento de este evento**, casi siempre `1`, no el acumulado. Cuando
  es mayor, `VideoActor` compara contra la hora del evento anterior de esa puerta (`lastEvent`) y
  deja WARN si los cruces empezaron antes del inicio del clip — el caso del binario recién
  arrancado, donde el acumulado de la cámara avanzó mientras nadie escuchaba. El conteo sigue
  siendo correcto; lo que avisa es que el video no alcanza a respaldarlo.
- **`max_gap_s` y `fps_effective` dicen si el clip se ve fluido o congelado, sin abrirlo.** A
  20 fps lo normal es un hueco de 0.05 s. Medido en la misma cámara y la misma escena quieta:
  con H.264+ apagado, **241 fotos en 12 s y hueco de 83 ms**; encendido, **2 fotos en 12 s y
  hueco de 12.2 s** — el clip entero es un hueco, y pesa 8.4 veces menos (85 KB contra 718 KB).
  El binario deja WARN cuando el hueco pasa de un segundo, porque sin ese aviso se culparía al
  extractor. Ojo: esas cifras son con **escena quieta**; con una persona cruzando hay
  movimiento y el codec debería emitir fotos, pero eso está **sin medir**.

### Correlación con la plataforma (`event_id`)

Cada paso contado recibe un `uid` de ocho hexadecimales, generado en `CountingActor.sendCounted`
(campo 5 del `.proto`). Como ese método manda el **mismo mensaje** a `events` y a `video`, ambos
ven el mismo id sin coordinación extra. Ese id es la llave con la que la plataforma une el conteo
con su video, y viaja en dos mensajes de MQTT (detalle en la skill `mqtt-contract`):

1. `COUNTERSDOOR` al contar el paso, con `event_id`;
2. `COUNTERSDOORVIDEO` cuando el clip **ya está en disco**, con `metadata`, `clip` y el serial.

**El segundo mensaje existe por una razón de diseño, no por comodidad.** Al contar el paso todavía
no se sabe el nombre final del archivo ni si la extracción va a tener éxito —puede no haber
cobertura de grabación, faltar espacio o caerse la cámara—, así que anunciarlo ahí dejaría a la
plataforma esperando un archivo que quizá nunca exista. El `COUNTERSDOOR` solo promete el id; el
segundo mensaje confirma un hecho. Si no llega, ese paso no tiene video, sin ambigüedad.

Va un `COUNTERSDOORVIDEO` **por evento** aunque varios compartan el clip, para que el cruce del otro
lado sea 1 a 1 por `event_id` y no haya que desarmar listas.

**`-withoutEventID` omite ese campo del `COUNTERSDOOR`**, devolviéndolo al formato anterior a
1.0.32. Es una compuerta de compatibilidad con la plataforma, temporal y con fecha de retiro: se
agregó porque la plataforma rechazaba el mensaje al ver el campo nuevo, y sin ella no había forma de
probar el resto del binario en un vehículo sin romperle el conteo. Mientras está activo **el cruce
conteo↔video es imposible**, porque `event_id` es la llave. No toca el `COUNTERSDOORVIDEO`, que va
por otro tópico (`EVENTS/counterevents`) y todavía nadie consume.

Se implementa **no rellenando el campo**, que ya tenía `omitempty`, y no con un struct alterno: así
el JSON sale byte por byte como antes y las dos formas no pueden separarse con el tiempo sin que
nadie lo note. Hay una prueba que compara el conjunto de claves de las dos formas y falla si la
diferencia es algo más que `event_id`.

**Verificado al agregarlo**: `event_id` es el **único** campo nuevo en los mensajes desde 1.0.32 —
se comparó el conjunto de etiquetas `json:` de `events-actor.go`, `counting-actor.go` y
`doors-actor.go` contra `3d14a25`, la versión anterior. Lo que sí es nuevo son dos **tipos** de
mensaje completos, `CAMERATIME` y `COUNTERSDOORVIDEO`, pero van a `EVENTS/counterevents` y no al
`EVENTS/backcounter` que consume la plataforma.

### Mantenimiento de las cámaras (`-ntpServer`)

Un solo actor (`CameraActor`) y un solo ciclo hacen **cinco** cosas por cámara, en este
orden: hora y NTP, horario de grabación, salud del almacenamiento, perfil de codificación, y
—solo si hace falta— el reinicio. El orden no es casual: el perfil va al final porque es el
único que puede terminar en un reinicio, así que una cámara que ya falló en algo anterior no
llega a que se le pida uno.

El actor se crea si se pide **cualquiera** de sus trabajos: `-ntpServer` o alguno de los flags del
perfil de codificación. Y como el video, exige además `-camera` y credenciales. Adentro cada bloque
se autolimita: **sin `-ntpServer` no se toca el reloj ni los servidores NTP** —la deriva se mide
igual, para el reporte— pero el horario de grabación sí se alinea, porque sin grabación no hay
video que extraer. Flags: `-ntpPort` (123), `-ntpInterval` (60m), `-timeZone`
(`CST+5:00:00`), `-timeDriftMax` (10s), `-cameraCheckInterval` (30m).

**Corrige la configuración pero NUNCA escribe el reloj.** Escribirlo obliga a pasar por
`timeMode: manual` y restaurar NTP después, y un salto de reloj hacia atrás es justo lo que
hace que `ListenActor` descarte eventos. Ante una deriva que persiste, alarma y deja que la
corrija el NTP de la cámara.

Cada ciclo, por cámara: lee `/ISAPI/System/time` y `/ISAPI/System/time/ntpServers`, escribe
**solo lo que difiere** del estándar, y mide la deriva. Medido en un DS-2XM6825G0 que venía
con `synchronizeInterval` en **1500 minutos** —25 horas entre sincronizaciones, con NTP bien
configurado y derivando igual—: el primer ciclo lo corrigió a 60 y el segundo verificó sin
escribir nada.

Cinco reglas que explican el diseño:

- **Idempotencia**: si `timeMode`, `timeZone`, servidor, puerto e intervalo coinciden, no se
  toca. Sin eso serían un PUT por cámara cada media hora, para nada.
- **Un 401 apaga esa cámara hasta el próximo arranque.** La cámara bloquea el usuario tras
  varios fallos, así que insistir cada ciclo con una credencial mala la deja inaccesible en
  campo. `isapi.ErrUnauthorized` es terminal por la misma razón.
- **`statusCode 7` no es un error, es "aplicado, falta reiniciar".** El cliente `isapi` lo
  reporta como error porque `OK()` solo acepta 0 y 1; el actor lo interpreta como "aplicado,
  falta reiniciar" y reinicia la cámara en el mismo ciclo (abajo).
- **La referencia horaria es el GPS** cuando hay trama válida (`gpsnmea` da `TimeStamp` y
  `DateStamp` en UTC), corrigiendo por la mitad del RTT; si no, el reloj del gateway, y el log
  dice cuál usó. Se prefiere el GPS porque es independiente del reloj que se está auditando.
- **Alarma al tercer ciclo consecutivo** fuera de tolerancia, no al primero, y el test del
  servidor NTP se consulta **solo** cuando hay deriva: así distingue problema de red de
  problema de reloj sin molestar a la cámara en cada ciclo.

Ojo con `timeZone`: es POSIX y **el signo está invertido**. `CST+5:00:00` significa UTC−5, que
es lo correcto para Colombia; escribirlo como `-5:00:00` deja la cámara diez horas corrida.

### Horario de grabación y salud del almacenamiento

Flags: `-recordStart` (`04:00:00`) y `-recordEnd` (`23:59:00`); vacías dejan el horario como
esté. **Un evento fuera de la ventana de grabación no tiene video posible**: la cámara de
laboratorio venía con `07:55-16:02` y dejaba media jornada sin nada que extraer.

Y no conviene poner 24/7 sin pensarlo. Con 507 kbps medidos, una SD de 8 GB da **1.4 días de
retención grabando todo el día contra 1.7 grabando veinte horas**. La palanca real de la
retención es el tamaño de la tarjeta, no el horario; las cuatro horas que quedan libres
compran poco disco.

Dos precisiones del endpoint (`/ISAPI/ContentMgmt/record/tracks/101`):

- **se aplica en caliente**: responde `statusCode 1` y al leerlo de vuelta ya está activo. No
  lo confundas con `/Streaming/channels/101`, que responde 7 y exige reiniciar;
- **la cámara redondea al minuto**: pedir `23:59:59` queda guardado como `23:59:00`. Por eso
  `isapi.SetScheduleWindow` compara **por minuto** — comparando por segundo daría diferencia
  siempre y escribiría en cada ciclo.

Se trabaja sobre el **XML crudo** y solo se sustituyen los `<TimeOfDay>`: el documento trae
regiones, calibración y overlays, y reconstruirlo desde un struct con los campos que interesan
borraría todo lo demás.

El almacenamiento (`/ISAPI/ContentMgmt/Storage`) solo se **reporta**, nunca se formatea:
formatear borra todo lo grabado y esa no es decisión de un binario. Vale la pena porque una SD
en `unformatted` hace que **ningún** clip se pueda extraer, y sin este aviso se culparía al
extractor. Va al evento `CAMERATIME` como `storage` y `storage_free_mb`, con el estado
`storage` al fallar y `storage_ok` al recuperarse — una publicación por transición, nunca por
ciclo.

**Se revisan TODOS los medios, no solo el primero** (`StorageStatus.Primero` devuelve el primero
que no esté sano). La cámara verificada reporta la lista con `size="8"` aunque traiga uno solo;
con dos, una SD rota en segunda posición quedaba invisible.

**"SD llena" NO es una falla, y por eso no se alarma por espacio libre.** Lo decide
`LoopEnable` del track:

| `LoopEnable` | Qué hace al llenarse | ¿Alarma? |
|---|---|---|
| `true` | borra lo más viejo y sigue grabando | **no** — el espacio libre tendiendo a cero es lo normal |
| `false` | **deja de grabar**, y ningún evento tiene video | **sí** |

Alarmar por `freeSpace` bajo daría un falso positivo en toda la flota a los pocos días. Mirando
la configuración en cambio, la falla se ve el día uno y no cuando ya se perdió la grabación.
Medido en el DS-2XM6825G0: `LoopEnable true`, 1536 MB libres de 7695, `workMode quota`.

Con `LoopEnable false` el campo `storage` viaja como **`nooverwrite`**. Es un **valor** nuevo en
un campo que ya era de texto libre (`ok`, `unformatted`, `error`, `idle`), **no una clave nueva**:
la forma del JSON no cambia. El log sí separa los dos casos, porque piden acciones distintas —
formatear no arregla una sobrescritura apagada.

El track se descarga **una sola vez por ciclo** y sirve para el horario y para `LoopEnable`: son
el mismo documento. Si falla, solo detiene el ciclo cuando había horario que alinear; sin
`-recordStart`/`-recordEnd`, `LoopEnable` queda sin mirar y el resto sigue. Y si la cámara no
reporta el campo, **no se alarma por omisión**: ausente no es lo mismo que apagado.

### Perfil de codificación y reinicio (`-smartCodec`)

Flags, todos vacíos o en cero por defecto = **dejar la cámara como esté**: `-smartCodec`
(`off`/`on`), `-videoCodec` (`h264`/`h265`), `-videoFrameRate` (fps), `-videoGop` (cuadros),
`-videoQuality` (1..100) y `-videoBitrateMax` (kbps).

**Cualquiera de estos flags crea el `CameraActor` por sí solo**, sin necesidad de `-ntpServer`.
Antes solo lo creaba `-ntpServer`, así que un `-smartCodec off` sin él quedaba ignorado en
silencio. Sin `-ntpServer` no se toca el reloj ni los servidores NTP —la deriva se mide igual,
para el reporte— pero el horario de grabación **sí** se alinea, porque sin grabación no hay video
que extraer.

De las dos palancas de tamaño, la que manda es `-videoQuality` (el `fixedQuality` de ISAPI, que
viene en 40 y el modelo verificado admite 1, 20, 40, 60, 80 y 100). `-videoBitrateMax` es el
`vbrUpperCap`: acota el peor caso, no baja el tamaño típico — en la cámara verificada viene en
8192 kbps mientras graba a ~200, así que no ata nada.

Medido en la misma cámara, misma escena, `fixedQuality 40`, ventana de 12 s:

| codec | H.264+ | fps | hueco máx | **KB/s** | ¿se ve el cruce? |
|---|---|---|---|---|---|
| H.264 | on | 20 | 0.08 s | 25.9 | **a veces** — 18 de 23 clips congelados con tráfico real |
| H.265 | on | 20 | **10.2 s** | 2.9 | no |
| H.265 | off | 20 | 0.18 s | 28.2 | sí |
| H.264 | off | 20 | 0.08 s | 42.9 | sí |
| **H.265** | **off** | **12** | 0.18 s | **16.7** | **sí** |

**La combinación recomendada es la última**: `-videoCodec h265 -smartCodec off -videoFrameRate 12`.
Sale **35% más chica que H.264+** y **61% más chica que H.264 sin el `+`**, con el cruce siempre
visible. El `+` compraba tamaño a costa de no grabar; H.265 lo compra comprimiendo.

**`-smartCodec off` es el que importa.** H.264+ con escena quieta graba un keyframe cada
varios segundos y el clip parece congelado: medido, **49 frames con un hueco de 5.7 s** contra
**201 continuos** al desactivarlo. Fue la causa del "no se vio bien el paso de la persona".

**Lo que exige reiniciar es el CAMPO, no el endpoint.** Medido en el mismo
`PUT /ISAPI/Streaming/channels/101`: `GovLength` y `maxFrameRate` responden `statusCode 1` y se
aplican en caliente; `SmartCodec` responde `statusCode 7` y queda inerte hasta el reinicio.

Cuando hace falta, **la cámara se reinicia sola**. No hay ventana horaria y no hay flag para
habilitarlo: si pediste `-smartCodec off`, se aplica. Cinco cosas que explican el diseño:

- **El PUT y el reinicio van en el mismo ciclo, segundos aparte.** Un cambio que responde `7`
  queda **guardado pero inerte, y la cámara reporta el valor guardado, no el efectivo**. Si el
  reinicio quedara para después y el binario arrancara de nuevo en el medio —supervisión,
  despliegue—, el ciclo siguiente leería el valor nuevo, no vería diferencia, y nadie
  reiniciaría nunca: la cámara seguiría grabando con el ajuste viejo mientras la configuración
  y la lectura del API coinciden en decir lo contrario.
- **`considerReboot` no vuelve a comprobar ninguna precondición**, por lo mismo: hacerlo podría
  dejar el cambio ya escrito pero sin reiniciar.
- **El único freno es `-encoderMinUptime` (30 min por defecto, configurable).** El tope de "un reinicio por cámara"
  (`camTimeState.rebooted`) vive en memoria y se pierde al arrancar de nuevo, y este repo tiene
  historia de bucles de supervisión —el bug de `ctx.Parent()` nulo produjo 7 arranques
  seguidos—. Un binario en bucle nunca llega a los 30 minutos, así que nunca reinicia una
  cámara. **Hubo antes una ventana horaria `-rebootStart`/`-rebootEnd` y se quitó**: el gateway
  se alimenta del vehículo y de noche queda apagado, así que una franja de madrugada no la
  alcanzaba ningún ciclo y el ajuste no se aplicaba nunca. Cambiaba una falla silenciosa por
  otra.
- **El reinicio cuesta y va a caer en horario de servicio.** Corta unos 70 segundos de
  grabación —pedir un instante del hueco devuelve `500`— y durante el arranque la cámara no
  envía eventos, así que los pasajeros que cruzan ahí no se cuentan. Medido: vuelve a responder
  en ~2 minutos. Es aceptable porque ocurre **una vez por cámara**: en estado estable el perfil
  coincide y no se escribe nada. Si aparece seguido, algo no está persistiendo y hay que mirar
  el log.
- **Un `statusCode 1 OK` NO garantiza que el valor quedó.** Medido: pedirle 30 fps —que no
  está entre los que admite— responde **OK y guarda 24 en silencio**. Sin verificar, el ciclo
  siguiente vuelve a ver la diferencia, escribe otra vez, y queda un PUT por cámara cada media
  hora **para siempre**, con un log que dice "corregida" sin que nada se corrija. Por eso
  después de cada escritura se relee y se vuelve a medir la diferencia; si algo no quedó, se
  loguea ERROR, se publica `encoder_rejected`, y **no se vuelve a intentar en esa cámara** hasta
  el próximo arranque. La comprobación es genérica y no valida contra la lista de capacidades:
  así atrapa cualquier recorte silencioso, de este modelo o de otro. Y cuando no queda, **no se
  declara `fixed`**: decirle a la plataforma que se corrigió algo que la cámara no aplicó es
  peor que no decir nada.
- **`videoCodecType` se escribe en caliente**, `statusCode 1`, sin reiniciar. El extractor
  maneja H.264 **y** H.265, y elige según lo que la cámara ofrezca para cada tramo: la SD
  conserva grabaciones de antes del cambio, y cada clip queda con su codec anotado en el
  sidecar (`codec`) y en el log. El codec observado sigue viajando en el evento
  (`video_codec`, `video_fps`, `smart_codec`).
- **Un valor mal escrito en `-videoCodec` o `-smartCodec` detiene el arranque.** Ignorarlo en
  silencio dejaría la cámara con el ajuste viejo mientras la configuración dice otra cosa, y
  eso se descubre recién al ver un clip congelado.

El reinicio **se decide en el hilo del actor y se ejecuta en goroutine**: `considerReboot` marca
`rebooted` antes de lanzar el PUT, así el candado se cierra sin carrera, y `msgRebootDone` trae
el resultado. Un fallo del PUT **no** reabre el candado: si el PUT falló pero la cámara igual se
reinició, insistir la reiniciaría dos veces.

Lo que el reinicio **no** rompe: `ListenActor` es un servidor HTTP pasivo, así que la cámara
vuelve a postear sus eventos sola cuando termina de arrancar. No hay nada que reconectar.

Verificado de punta a punta contra el DS-2XM6825G0: perfil ya alineado → no escribe; `GovLength`
distinto → `statusCode 1`, en caliente, sin reinicio; `SmartCodec` distinto → `statusCode 7`,
reinicio, cámara de vuelta en 2 min con el ajuste efectivo, y el ciclo siguiente idempotente.

### Apagado de la cámara por GPIO (`-cameraPowerGPIO`)

Deshabilitado salvo que se pase `-cameraPowerGPIO`: sin él no se crea el actor, no se
escucha nada y el binario se comporta como antes de que esto existiera. Flags:
`-cameraPowerOffLevel` (1), `-cameraPowerOffDelay` (20s), `-cameraBootDelay` (2m).

**La línea y la polaridad son configurables a propósito**, porque dependen del cableado
de cada equipo y no se pueden adivinar desde el binario. `-cameraPowerGPIO` es el número
de línea y `-cameraPowerOffLevel` el nivel que significa APAGAR; el de encender se
**deriva** como complemento, así que con un solo número se invierte toda la lógica y no
existe la combinación imposible de dos flags que se contradigan.

**Y hay un modo utilitario para averiguar la polaridad sin arriesgar nada**, porque
equivocarse es caro: si `-cameraPowerOffLevel` queda al revés, el binario arranca
poniendo la línea en lo que cree que es "encendida", la cámara se queda sin energía, y el
`CAMERAPOWER` informa `poweron` mientras nadie cuenta.

```bash
./hikvision -cameraPowerGPIO 165 -cameraPowerSet 1   # deja la línea en 1 y TERMINA
```

No levanta el conteo, relee el valor en vez de confiar en la escritura, y dice qué
configurar según lo que se observe en la cámara. Es para que el equipo de apoyo pruebe
los dos niveles contra el vehículo real y recién después fije el flag.

**La señal viene de `go-ignition` por MQTT**, no por protoactor remote. Ese binario
corre en el mismo gateway (`/usr/sbin/omvz7/ignition`) y publica en `EVENTS/ignition`:

```json
{"value":{"state":1},"timestamp":1793335980.405281,"type":"SUSPEND_EVT"}
{"value":{"state":1},"timestamp":1793335980.401813,"type":"IgnitionEvent"}
```

Estados: `NA=0, UP=1, DOWN=2`. **Solo se actúa sobre `SUSPEND_EVT`.** Ese es el que
`go-ignition` emite junto a su `PowerEvent` interno, o sea **después de su propio
periodo de gracia** tras la llave: ya viene antirrebote. El `IgnitionEvent` es el estado
crudo y solo se registra — actuar sobre él apagaría y encendería la cámara con cada
golpe de llave, y cada encendido cuesta dos minutos sin contar a nadie.

`go-fareCollection` resuelve lo mismo con protoactor remote (`DiscoverIgnition` →
`IgnitionPowerSubscription` → `PowerEvent` tipado). **Acá se eligió MQTT a propósito**:
este binario no usa remote, y habilitarlo serían un puerto TCP nuevo, serialización
remota, un handshake con reintentos y un `ctx.Watch` sobre un PID remoto. `SubscribeFun`
ya existe y `DoorsActor` ya hace exactamente esto. Lo que se paga es un contrato más
flojo —JSON y un string mágico—, y por eso hay una prueba con el payload real capturado
del gateway, igual que `g1_test.go` con el XML de la cámara.

**Ojo con el `timestamp`**: en este JSON son **segundos con fracción**, mientras que el
`PowerEvent` que el mismo binario manda por protoactor los lleva en **milisegundos
enteros**. Mismo nombre, distinta unidad.

**La GPIO se escribe a sysfs a mano.** `github.com/brian-armstrong/gpio` estaba en
`go.mod` con un watcher de entradas que **nadie llamaba**; se quitó junto con la
dependencia. No servía para esto: tiene **siete `os.Exit(1)`** ante cualquier fallo al
abrir un archivo de sysfs, así que un pin mal escrito en la línea de comandos mataría el
proceso de conteo entero —sin panic, sin log, y sin que la supervisión lo distinga de
una salida limpia—. `SalidaGPIO` devuelve los errores, y hay una prueba que lo fija.

**El pin de la cámara es el `gpio165` y el nivel de apagado es `1`** (confirmado por
Camilo). Eso coincide con `/usr/sbin/omvz7/switch.sh`, el script del propio equipo, que
exporta ese pin y le escribe `1`: es un apagado manual de la cámara. Como
`-cameraPowerOffLevel` ya trae `1` por defecto, en el lanzador alcanza con
`-cameraPowerGPIO 165`.

Dos cosas medidas en el equipo que explican el manejo del export:

- **el pin arranca sin exportar** después de cada arranque del gateway, y la cámara está
  encendida igual: el estado por defecto del hardware es "encendida", que es la dirección
  segura;
- **`switch.sh` lo deja exportado**, así que si alguien lo corrió antes, el
  `echo 165 > export` del binario falla con EBUSY. Eso **no** es un error: lo que se
  comprueba es que el directorio del pin exista y se pueda escribir. Tratarlo como fallo
  dejaría al binario sin control de la cámara por un script que alguien corrió antes.

La dirección se fija escribiendo **`low`/`high`, no `out`**: sysfs lo trata como "salida
con este valor inicial" en una sola operación. Escribir `out` y después el valor deja la
línea un instante en el nivel anterior, y ese parpadeo sobre un relé apagaría la cámara
al arrancar el binario. El nivel de "encendida" **se deriva** como complemento de
`-cameraPowerOffLevel`, para que no exista la combinación imposible de dos flags que se
contradigan. Y después de escribir se **relee**: sysfs acepta la escritura y el valor
puede no quedar, igual que pasa con el perfil de codificación de la cámara.

**Pero esa relectura NO es terminal, y eso se decidió midiendo.** En FLO-W7-0138 (i.MX6,
kernel 4.1.15), el `gpio165` acepta `direction=out`, acepta el write a `value`, y `value`
lee `0` siempre.

**El sysfs NO alcanza para saber qué pasa**, y confundirlo cuesta: el driver de i.MX
(`gpio-mxc.c`) registra el **PSR** como registro de lectura, así que `value` devuelve el
nivel **eléctrico del pad**, no lo que pedimos. Leyendo los registros del bloque por
`/dev/mem` queda claro (GPIO6 en `0x020B0000`, `GPIO6_IO05` = bit 5):

```
         con value=1 pedido          tras volver a 0
GDIR     bit 5 = 1  (SALIDA)         bit 5 = 1
DR       bit 5 = 1  ← lo pedido      bit 5 = 0
PSR      bit 5 = 0  ← lo que ve      bit 5 = 0
```

**El SoC sí maneja el pin**: el registro de datos tiene exactamente lo que le pedimos, en
los dos niveles. Lo que no lo confirma es la lectura. Con la GPIO en la **base de un
transistor** eso es lo esperado: la unión base-emisor clampea a ~0.7 V y un pad de 3.3 V
lo lee como nivel bajo. Un pad sin el camino de entrada habilitado (SION) daría lo mismo.

O sea que la diferencia es **ambigua desde el software y siempre lo va a ser**. Lo único
que la resuelve es un multímetro en la base del transistor.

Esa lectura de registros está en el propio binario, para no tener que copiar nada al
equipo cuando haga falta diagnosticar:

```bash
./hikvision -gpioRegisters 165
```

Solo lee, no escribe. Va en este binario por conveniencia operativa —es el que ya está
desplegado en todos los gateways— pero **no es una herramienta de Hikvision**: no sabe
nada de cámaras, es específica del SoC y le serviría igual a `go-ignition` o `go-doors`.
Si otro proyecto la necesita, el lugar correcto es tooling compartido, no copiar
`client/main/gpioregisters.go`.

Usa `mmap` y no `read` porque en ARM las regiones de periféricos no están en el mapa
lineal del kernel: un `dd if=/dev/mem` falla con `Bad address` aunque
`CONFIG_STRICT_DEVMEM` esté desactivado.

Por eso `ErrNivelNoVerificado` es un error **aparte** y quien llama recibe igual una
`SalidaGPIO` usable: al arrancar se deja un WARN y se sigue. Tratarlo como falla
detendría el binario entero, y **el conteo de pasajeros no se negocia por el cableado de
un accesorio**. Lo que sí es terminal es no poder exportar la línea o no poder fijarle la
dirección: ahí de verdad no la controlamos.

Secuencia, con el plazo en medio a propósito:

```
SUSPEND_EVT DOWN → callar a ping, camera y video
                 → esperar -cameraPowerOffDelay (que termine el clip en curso)
                 → GPIO al nivel de apagado
                 → publicar CAMERAPOWER status "poweroff"

SUSPEND_EVT UP   → GPIO al nivel de encendido (inmediato)
                 → esperar -cameraBootDelay (la cámara tarda ~2 min en arrancar)
                 → rehabilitar ping, camera y video
                 → publicar CAMERAPOWER status "poweron"
```

Primero se calla y después se corta: al revés, el plazo del clip serían también veinte
segundos de keep-alive fallando contra una cámara que ya no está.

**Apagar la cámara sin avisarle a los tres actores que le hablan haría que empiecen a
mentir**, y es la parte que una implementación ingenua se saltea:

| actor | qué haría sin la compuerta |
|---|---|
| `PingActor` | `CounterDisconnected` cada 300 s — el falso positivo que se arregló en 347c7ce |
| `CameraActor` | fallos de `storage`, deriva y `encoder_rejected` cada media hora |
| `VideoActor` | RTSP cortado a mitad de extracción |

Los pendientes de video se **descartan** al apagar, por la misma razón que con
`MsgClockStep`: ya no se pueden cumplir. Ahí la marca de tiempo dejaba de apuntar al
tramo correcto; acá el tramo no va a existir, porque la cámara deja de grabar.

**Los temporizadores se validan contra el estado al llegar.** Si el vehículo vuelve a
encender dentro del plazo de corte, el `msgCortarAlimentacion` que ya estaba en camino
**se descarta**: sin esa guarda le cortaría la energía a la cámara de un vehículo en
servicio. Lo mismo al revés con el arranque. Hay una prueba por cada caso.

**Y es idempotente**: `go-ignition` reemite el estado con cada evento de llave y al
suscribirse. Sin esa guarda, cada repetición de un `UP` reiniciaría la espera de
arranque y la cámara quedaría marcada como no disponible indefinidamente, contando cero
con el vehículo en servicio.

**Los acumulados de la cámara se reinician al perder energía, y no hace falta hacer
nada**: `CountingActor` reancla `rawXmap[id]` después de toda la cadena if/else, incluso
cuando descarta el paso, así que se pierde a lo sumo el **primer** cruce tras el arranque
y el siguiente ya cuenta bien. Las reglas de conteo no se tocaron.

Un valor mal escrito en cualquiera de los cuatro flags **detiene el arranque**, igual que
`-videoCodec`: seguir sin control de energía dejaría la configuración diciendo una cosa y
el equipo haciendo otra, y eso se descubre cuando la batería ya se gastó.

### Configuración desde la plataforma (`DEV_CAMERACOUNT_CONFIG`)

Los parámetros que más se tocan —IP y puerta de cada cámara, la polaridad de cada puerta,
NTP, perfil de codificación, interruptor del video y GPIO de apagado— se pueden configurar
desde la plataforma sin entrar por SSH a editar `/etc/init.d/backdoor`. El contrato completo
—esquema del JSON, qué pasa ante cada fallo, cómo viaja— está en
[docs/contrato/configuracion-plataforma.md](docs/contrato/configuracion-plataforma.md).
Acá va lo que hace falta para tocar el código.

**La prioridad es la INVERSA de la del resto de la flota, y es una decisión, no un descuido:**

```
1. flag pasado explícitamente en la línea de comandos  -> gana siempre
2. DEV_CAMERACOUNT_CONFIG                              -> solo rellena lo que el flag no fijó
3. default compilado
```

En `go-driverconsole` y `go-counterpass` la plataforma pisa el flag sin preguntar, porque su
merge compara contra el **valor actual de la variable** y no sabe de dónde salió — no hay un
solo `flag.Visit` en ninguno de los dos. Acá sí se sabe: `flagsPasados()` recorre únicamente
los flags realmente escritos. **No se puede deducir del valor**: `-videoFrameRate 0` y no
pasar el flag dejan exactamente la misma variable, y esa confusión es la que rompe el merge
de los otros binarios.

**La consecuencia operativa hay que decirla**: el lanzador de hoy pasa casi todos estos
flags, así que la plataforma no mueve nada hasta que se los quite. Por eso el banner de
arranque y cada aplicación registran, parámetro por parámetro, de dónde salió el valor
(`Efectiva.Describir`).

**Todos los campos escalares del JSON son punteros**, y eso tampoco es estilo. Un `bool`
ausente se deserializa como `false` y un `int` como `0`; sin punteros no hay forma de
distinguir "la plataforma no mandó el campo" de "mandó false". Medido en los binarios de
referencia: un objeto que **omite** un booleano lo apaga igual, pisando el flag con el que se
arrancó el equipo, y encima dispara un reinicio de actores.

**`cameras` reemplaza la lista entera, no es un parche.** Mandar una sola cámara deja la otra
puerta sin cámara. Es lo que hace falta para poder quitar una, pero usarlo mal no da ningún
error: se ve como una serie de conteo que se congela.

**La puerta y sus dos switches van dentro de cada cámara.** En la línea de comandos son tres
flags repetibles que hay que mantener alineados por posición, y desalinearlos descarta los
cruces de una puerta durante un turno entero sin un solo error. En el JSON eso no se puede
escribir mal.

#### Se aplica en caliente, y el conteo no se toca

`CountingActor` recibe `MsgAplicarConfig` y reconstruye **solo** los hijos que el cambio
afecta (`Efectiva.Diff` agrupa por actor, no por campo). Los acumulados, la boltdb y la
persistencia viven en `CountingActor`, que no se reinicia: un cambio de `videoFrameRate` no
puede costar un contador.

Por eso `SetVideoProps`/`SetCameraProps` dejaron de existir y hoy son **fábricas**
(`SetVideoFactory`, `SetCameraFactory`): unas `Props` armadas en main dejan la configuración
congelada y reconstruir el actor con otros valores exigiría reiniciar el proceso. Una fábrica
que devuelve `nil` significa "este hijo no va", que es como se apaga el video o el
mantenimiento de cámara — el mismo camino que ya existía cuando faltaba `-videoDir`.

Tres cosas del respawn que no son obvias:

- `PoisonFuture().Wait()` **bloquea el hilo de `CountingActor`** hasta que el hijo termine.
  Los eventos que lleguen mientras tanto esperan en el buzón, no se pierden.
- **un hijo recién creado nace creyendo que la cámara está encendida**; si estaba apagada por
  ignición hay que mandarle `MsgCameraPower{On:false}`, o se pone a hablarle a una cámara sin
  energía y a reportar averías que no existen;
- un fallo del respawn **no entra en pánico**. `CountingActor` es el actor de persistencia:
  reiniciarlo replica la boltdb y republica acumulados recalculados, que es un precio
  desproporcionado por no haber podido reconstruir un accesorio.

**`ListenActor.cameras` quedó detrás de un `sync.RWMutex`.** Lo lee la goroutine del servidor
HTTP, una vez por evento en `doorID`, y ahora lo escribe `Receive`. Sin el candado es una
carrera sobre el dato que decide a qué puerta va cada pasajero.

**Un cambio de GPIO deja primero la línea vieja en el nivel de "encendida"** y recién después
toma la nueva. Al revés quedaría una línea abandonada manejando el nivel de apagado, y con el
relé conectado eso es una cámara que no vuelve nunca.

#### Qué se rompe si falla, y qué no

Nada de esto detiene el conteo. Sin broker, sin `nats-gwIot` o sin sesión con la plataforma,
el binario cuenta con los flags, los defaults y el cache. Hay **una** excepción deliberada en
la otra dirección: si `cameraPowerGPIO` viene de un **flag** y no se puede tomar la línea, el
binario se detiene —quien lo lanzó escribió algo que el equipo no puede hacer—; si viene de la
**plataforma**, se deshabilita solo el apagado y se sigue contando. Detenerse ahí dejaría a
toda la flota sin arrancar, en bucle de supervisión, y sin forma de corregirlo desde el
lanzador.

#### El cache en disco es propio, porque el de `go-params` no funciona

`go-params` **escribe** el mapa plano crudo en boltdb y lo **lee** como un
`messages.ConfigParam` —otro esquema—, con lo que recupera un struct vacío; y el valor leído
nunca se propaga al padre (la única rama que lo haría responde a un mensaje que nadie envía).
Un arranque sin red queda sin configuración aunque el archivo esté ahí.

Acá la persistencia de `go-params` se deshabilita y se guarda el JSON crudo en
`-platformConfigCache` (`/SD/boltdbs/cameracount-config.json`), como `.part` + rename, que es
atómico. **Hace falta de verdad**: con los flags quitados del lanzador, un arranque sin red
dejaría el conteo con el default de `zeroOpenState`, que descarta **todos** los cruces en un
vehículo cableado al revés.

#### El descubrimiento está reimplementado a propósito

`go-gwiot/pkg/gwiot.NewDiscoveryActor` son cuarenta líneas, pero vive en el mismo paquete Go
que el cliente completo de NATS: importarlo enlaza `nats.go`, `go-oidc`, `oauth2` y
`keycloak` dentro de un binario que **nunca abre una conexión a NATS** —de eso se encarga
`nats-gwIot`, que corre aparte y tiene las credenciales—. `client/parametros/discovery.go`
copia el actor e importa solo `pkg/gwiotmsg` (protobuf puro) y `pkg/gwiotmsg/gwiot` (dos
constantes), así que el protocolo sigue siendo el mismo y compartido.

Lo que **no** se pudo evitar es **gRPC**: protoactor `remote` lo arrastra entero. Medido en
armv7: **20.0 MB → 25.8 MB, +5.8 MB (+29%)**. Es el precio de la función, y conviene saberlo
antes de agregarle otra.

**`DEV_CAMERACOUNT_CONFIG` exige un cambio en `go-params`.** Su parseo recorre el mapa con un
`switch k` cuya rama `default` está vacía: **una clave que no esté listada ahí se descarta en
silencio, sin log**. Publicar el atributo del lado de la plataforma no alcanza.

### Credenciales de cámara

Van en la variable de entorno `HIKVISION_CREDENTIALS`, cifradas con AES-GCM y una llave embebida
en el binario ([client/main/credentials.go](client/main/credentials.go)). El valor se genera con el
propio binario, leyendo usuario y clave por stdin para que no queden en la lista de procesos:

```bash
printf 'admin\nLA-CLAVE\n' | ./hikvision -encryptCredentials
# HIKVISION_CREDENTIALS=kCUGnjpq...
```

**Esto es ofuscación, no secreto**: la llave está en el binario, así que quien lo tenga recupera
las credenciales, igual que quien pueda leer `/proc/<pid>/environ`. Fue una decisión explícita, y
lo que compra es que la clave no quede legible en el unit file, el script de despliegue o un
volcado de logs. No la presentes como control de seguridad.

Sin la variable el binario arranca igual y solo deshabilita el diálogo con la cámara; un valor
corrupto deja WARN y tampoco detiene el conteo.

## Convenciones del código

- **Actores**: struct con `*Logger` embebido, `New...()` que inicializa mapas, y `Receive`
  con `switch ctx.Message().(type)` que cubre `*actor.Started` (llama `act.initLogs()`) y
  `*actor.Stopping`. Ver la skill `actor-conventions`.
- **`Panicln` es intencional**: `errLog.Panicln(...)` / `logs.LogError.Panic(...)` se usan para
  provocar la supervisión de protoactor (reinicio del actor), casi siempre precedidos de
  `time.Sleep(3 * time.Second)` para no entrar en ciclo cerrado de reinicios. No los conviertas
  en `return err` sin entender la estrategia de supervisión.
- **Logs**: cinco niveles inyectados (`errLog`, `warnLog`, `infoLog`, `buildLog`, `cameralog`).
  `buildLog` se descarta salvo con `-debug`; `cameralog` solo escribe a `/SD/logs/camera*` con
  `-logxml`. Sin `-logStd` todo va a syslog.
- **Mensajes**: los que cruzan persistencia o red son protobuf (`client/messages`); los internos
  son structs planos en el paquete `client` (`MsgDoor`, `MsgGetGps`, `msgEvent`, `msgPingError`).
- Los `fmt.Printf` sueltos en `listen-actor.go` y `listenner.go` son depuración dejada en
  caliente; no los tomes como patrón.

## Deuda conocida (no "arreglar" de paso sin acordarlo)

Desviaciones respecto a la especificación ISAPI, con su estado medido contra una cámara real
(DS-2XM6825G0, V5.5.850). Detalle y evidencia en la skill `hikvision-isapi`.

Corregidas en **1.0.30**:

- el handler aceptaba solo `Content-Type: application/xml` y descartaba `text/xml` **en silencio
  con HTTP 200**. Ahora acepta cualquier media type con `xml` y **loguea WARN al descartar**, que
  era la mitad valiosa: un `multipart` o un JSON ya no desaparecen sin rastro;
- `statisticalMethods: signalTrigger` no se filtraba. No duplicaba: **corrompía** el acumulado en
  cascada, porque esos eventos traen conteo de ventana y no acumulado. Ahora se descarta junto con
  `timeRange`, con lista negra y no blanca (el nodo es opcional en ISAPI: una lista blanca
  descartaría todo evento de una cámara que lo omita).

Pendientes:

- **corregido** — un POST `multipart/form-data` **ya se parsea**. Dejó de ser latente el
  2026-10-30, cuando la G1 pasó a firmware V4.2.2: desde esa versión adjunta la foto del cruce y
  manda `multipart/form-data; charset="UTF-8"; boundary=...`, que no contiene `xml` y caía en el
  descarte. Esa puerta dejó de contar **sin que nada fallara a la vista** — la cámara recibía 200
  y solo quedaba el WARN. Ver "El evento puede venir en multipart" abajo;
- **no ocurre** — el tamper se cuenta por notificación sin filtrar `eventState`, pero el trigger
  está en `notificationRecurrence: beginning`. Sin verificar si `notificationMethod: center`
  llega siquiera al httpHost.

Verificado y **no toques**: el regex de `parseDateTime` es indispensable. La cámara emite
`dateTime` con offset `-5:00` (sin cero inicial), que `time.Parse(time.RFC3339, ...)` rechaza; sin
esa normalización se descartaría todo evento.

**Relojes de cámara.** `ListenActor` compara el `dateTime` de cada evento contra el último visto
para ese `id` (`timeBefore`, en memoria). Un paso atrás **menor** a `resyncThreshold` se descarta
como desorden de entrega; uno **mayor** se acepta y reancla la referencia, porque es el reloj de la
cámara siendo corregido. Sin esa distinción, una corrección de NTP hacia atrás descartaba en
silencio todos los eventos de esa puerta hasta que el reloj recuperara la marca anterior — y con
`synchronizeInterval` en horas eso es perder un turno entero. Tenlo presente antes de ajustar
NTP en la flota: corregir la hora **provoca** ese salto.

Y del lado del repo:

- `go test ./...` **pasa completo**, y conviene que siga así. Hasta la 1.0.39 fallaba siempre por
  lo mismo: [peoplecounting/listenner_test.go](peoplecounting/listenner_test.go) era un esqueleto
  de `gotests` con `want: nil` sin completar — el parser funcionaba, el caso de prueba nunca se
  terminó de escribir. Ya está completo y cubre lo que no depende del modelo de cámara: cada
  `eventType` en su rama, los dos nombres del tamper, el cuerpo sin declaración `<?xml ?>` y los
  cuerpos que deben fallar. Un `FAIL` global ahora **sí** es una regresión.
- Código muerto conservado a propósito: `client/_business/` (copia previa de los actores),
  `client/pubsub-actor.go_`, `client/service/service.go_`.
- `client/comm/grpc`, `client/comm/pubsub` y `client/service` definen una interfaz de servicio
  que `client/main` **no usa**.
- `MapInterface`/`MyMap` en [client/map.go](client/map.go) no se usa; los mapas de `CountingActor`
  se acceden sin lock (correcto: siempre desde el hilo del actor).
- `ListenActor` no cierra el servidor HTTP salvo por `ctx.Done()`; el `close(ch)` en
  `Listen` corre incluso en shutdown limpio y termina en `msgListenError` → panic → restart.
- **`Publish` tiene una carrera con el actor de MQTT**, detectada con `go test -race` al
  agregar el apagado por GPIO. `Publish` ([client/pubsub.go:93](client/pubsub.go#L93)) lee
  `instance.ctx` desde el hilo de quien llama, mientras `pubsubActor.Receive`
  ([client/pubsub.go:137](client/pubsub.go#L137)) reasigna `ps.ctx` **en cada mensaje**. Es
  anterior a este trabajo y no se tocó: arreglarlo bien significa dejar de guardar el
  contexto en el struct, y eso es el singleton que usa todo el binario. Mientras tanto
  **ninguna prueba debe llamar a `Publish`**: además de la carrera, levanta el cliente de
  MQTT e intenta conectarse a `127.0.0.1:1883`. `CountingActor.publicar` existe para
  desviarlo en las pruebas.

**Corregido en 1.0.30, vale saber que existió**: `CountingActor` pedía el GPS con
`ctx.RequestFuture(ctx.Parent(), ...)` al manejar `*msgPingError`, pero es un actor de raíz y
`ctx.Parent()` es `nil` — pedirle a un PID nulo hace panic. Cada fallo del keep-alive de la cámara
reiniciaba el actor, replicaba la boltdb y republicaba acumulados recalculados (medido: 7 arranques
y `inputs0` yendo 2 → 3 → 1 → 3), mientras el evento `CounterDisconnected` nunca llegaba a
publicarse. Si aparecen contadores que retroceden en datos históricos, este es el sospechoso.
Ojo con el patrón: `ctx.Parent()` **sí** es correcto en los hijos (`EventActor`), no en `counting`.

## Documentación de referencia (`docs/`)

**[`docs/contrato/`](docs/contrato/)** documenta las dos fronteras hacia afuera, para quien
implemente del lado de la plataforma: los eventos de MQTT (cuáles son nuevos y a cuáles se les
agregaron campos), la ruta y el contenido de lo que queda en el bucket, y **cómo se identifica un
clip** desde un evento y desde el JSON. Si tocás el contrato, actualizalo junto con la skill
`mqtt-contract` — la skill es para quien trabaja en este repo, el `docs/contrato/` es para quien
consume lo que publica.

| PDF | Qué es | Aplica |
|---|---|---|
| `ISAPI_peopleCounting.pdf` (89 p.) | guía oficial de conteo de personas: httpHosts, triggers, evento `PeopleCounting`, endpoints | **sí, es el documento clave** |
| `ISAPI_general.pdf` (946 p.) | autenticación Digest, modelo de URI, `ResponseStatus`, catálogo de `eventType`, endpoints de sistema | sí |
| `ISAPI_thermal.pdf` (275 p.) | cámaras térmicas | no |
| `HikVision Player SDK for linux v4.3.pdf` | Player SDK C/SDL (`Hik_PlayM4_*`), reproducción de video, 2008 | **no es ISAPI**, no aplica |

La skill `hikvision-isapi` destila lo relevante de los dos primeros, y
`.claude/skills/hikvision-isapi/references/endpoints.md` tiene el índice de endpoints con método,
mensaje y sección del PDF. Se consultan con `pdftotext -layout`. El Player SDK requeriría binding
cgo a un `.so` propietario más SDL, y este binario nunca toca video.

## Al terminar un cambio

1. `go build ./... && go vet ./...`
2. **No toques `showVersion`**; se sube aparte, cuando hay acuerdo de desplegar.
3. Si tocaste el contrato MQTT o el XML de la cámara, actualiza la skill correspondiente
   (`mqtt-contract`, `hikvision-isapi`) — son la documentación viva de esas fronteras.
