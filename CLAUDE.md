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

**Dependencias por `replace`**: el `go.mod` apunta a repos hermanos en el filesystem
(`../go-doors`, `../go-actors`, `../../asynkron/protoactor-go`). Sin ellos no compila;
no sustituyas esos `replace` por versiones remotas sin acordarlo.

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
│   └── camera        CameraActor    → hora, NTP, grabación, SD y codec (solo con -ntpServer)
├── listenner         ListenActor    → servidor HTTP que recibe los eventos de la cámara
└── pubsub-actor-XXXX singleton MQTT (lo crea InitPubSub desde counting/Started)
```

El paquete [video/](video/) extrae un tramo grabado por RTSP y lo escribe como MP4, en Go puro
(`gortsplib` + `mediacommon`). Sin ffmpeg **a propósito**: el binario cross-compila a cualquier
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

Sigue hardcodeada `pingIP` en [client/ping-actor.go:13](client/ping-actor.go#L13); la absorberá el
actor de cámara cuando exista. Ojo con el ruteo: si los eventos llegan por NAT o port-forward, el
`RemoteAddr` es el del router y **todas las puertas colapsan en una** (verificado: con ruteo puro
la IP de origen se preserva).

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
(16s), `-videoQueue` (500).

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
el espacio libre baja de 512 MB, porque en la misma partición vive la boltdb del conteo.

Cuatro cosas medidas contra una cámara real que explican el diseño:

- **Pedir un instante sin grabación no da error: devuelve el tramo disponible más cercano.** Por
  eso `isapi.HasCoverage` es una guarda obligatoria antes de cada extracción. Sin ella un clip
  queda con el nombre de un evento y muestra otro momento.
- **La ventana termina después del evento**, así que al recibirlo esa parte todavía no está
  grabada. El actor espera `videoSettleDelay` tras el final de la ventana antes de pedirla; sin esa
  espera la verificación de cobertura falla siempre.
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
  "bytes": 1264740
}
```

Cuatro precisiones que evitan malinterpretarlo:

- **`event_time_ms` va en milisegundos pero la resolución de origen es de un segundo**: la cámara
  envía el `dateTime` sin fracción, así que siempre termina en `000`. No leas precisión que no hay.
- **`clip_start_requested` es el instante PEDIDO, no el primer frame.** La cámara posiciona la
  reproducción en el keyframe anterior, así que el video puede empezar hasta un GOP antes (medido:
  2 s a 20 fps). Da más pre-roll que el configurado, nunca menos. Lo mismo vale para `offset_s`:
  es aproximado al frame, no exacto.
- **`camera_serial` y `camera_mac` se consultan una sola vez por cámara** con
  `isapi.GetDeviceInfo` y quedan en caché. Si esa consulta falla los campos van vacíos pero **la
  extracción sigue**: el clip vale más que su metadato. Son la identidad estable del equipo,
  mientras `camera` es la IP y cambia con el direccionamiento.
- **`camera_counter` es el incremento de este evento**, casi siempre `1`, no el acumulado.

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

### Mantenimiento de las cámaras (`-ntpServer`)

Un solo actor (`CameraActor`) y un solo ciclo hacen **cinco** cosas por cámara, en este
orden: hora y NTP, horario de grabación, salud del almacenamiento, perfil de codificación, y
—solo si hace falta— el reinicio. El orden no es casual: el perfil va al final porque es el
único que puede terminar en un reinicio, así que una cámara que ya falló en algo anterior no
llega a que se le pida uno.

Todo el bloque está deshabilitado salvo que se pase `-ntpServer`, y como el video exige
además `-camera` y credenciales. Flags: `-ntpPort` (123), `-ntpInterval` (60m), `-timeZone`
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
  reporta como error porque `OK()` solo acepta 0 y 1; el actor lo interpreta y decide según
  la ventana de reinicio (abajo). Sin ventana configurada solo avisa, que es el default.
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
compran poco disco y, en cambio, sirven de **ventana de mantenimiento** para el reinicio.

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
extractor. Va al evento `CAMERATIME` como `storage` y `storage_free_mb`.

### Perfil de codificación y reinicio (`-smartCodec`)

Flags, todos vacíos o en cero por defecto = **dejar la cámara como esté**: `-smartCodec`
(`off`/`on`), `-videoFrameRate` (fps), `-videoGop` (cuadros), `-rebootStart`, `-rebootEnd`.

**`-smartCodec off` es el que importa.** H.264+ con escena quieta graba un keyframe cada
varios segundos y el clip parece congelado: medido, **49 frames con un hueco de 5.7 s** contra
**201 continuos** al desactivarlo. Fue la causa del "no se vio bien el paso de la persona".

**Lo que exige reiniciar es el CAMPO, no el endpoint.** Medido en el mismo
`PUT /ISAPI/Streaming/channels/101`: `GovLength` y `maxFrameRate` responden `statusCode 1` y se
aplican en caliente; `SmartCodec` responde `statusCode 7` y queda inerte hasta el reinicio.

Cinco decisiones que no son negociables sin volver a medir:

- **La ventana no es "cuándo reiniciar", es "cuándo escribir".** Sin
  `-rebootStart`/`-rebootEnd` el encoder **no se escribe en absoluto**: solo se avisa qué
  falta aplicar. Un cambio que responde `7` queda **guardado pero inerte, y la cámara reporta
  el valor guardado, no el efectivo**. Si se escribiera fuera de la ventana y el binario
  reiniciara antes de que llegue —supervisión, despliegue—, el ciclo siguiente leería el valor
  nuevo, no vería diferencia, y nadie reiniciaría nunca: la cámara seguiría grabando con el
  ajuste viejo mientras la configuración y la lectura del API coinciden en decir lo contrario.
  Escribiendo dentro de la ventana, el PUT y el reinicio pasan en el mismo ciclo.
- **`considerReboot` NO vuelve a comprobar la ventana.** Volver a comprobarla podría dejar el
  cambio ya escrito pero sin reiniciar, que es exactamente el estado que se está evitando.
- **El reinicio cuesta.** Corta unos 70 segundos de grabación —pedir un instante del hueco
  devuelve `500`— y durante el arranque la cámara no envía eventos, así que los pasajeros que
  cruzan ahí no se cuentan. Medido: vuelve a responder en ~2 minutos.
  `00:30:00-03:30:00` cae dentro del hueco que deja el horario de grabación por defecto.
- **Un reinicio por cámara por arranque del binario** (`camTimeState.rebooted`). Si un modelo
  acepta el PUT pero no lo persiste, el ciclo siguiente ve la misma diferencia y volvería a
  reiniciar: cada media hora, en toda la flota. Al segundo pedido el actor loguea ERROR y no
  reinicia — un reinicio que no arregla nada es algo para mirar en el log.
- **`videoCodecType` se reporta pero NUNCA se escribe.** `video/extract.go` solo maneja H.264;
  poner H.265 desde acá rompería la extracción en silencio. El actor deja WARN y el codec
  observado viaja en el evento (`video_codec`, `video_fps`, `smart_codec`) para que la
  plataforma detecte de lejos una cámara mal configurada.

La ventana es **exclusiva en el fin** y soporta cruce de medianoche (`22:00-04:00`): si fuera
inclusiva, una ventana que termina donde arranca la grabación permitiría reiniciar en el primer
segundo de servicio.

El reinicio **se decide en el hilo del actor y se ejecuta en goroutine**: `considerReboot` marca
`rebooted` antes de lanzar el PUT, así el candado se cierra sin carrera, y `msgRebootDone` trae
el resultado. Un fallo del PUT **no** reabre el candado: si el PUT falló pero la cámara igual se
reinició, insistir la reiniciaría dos veces.

Lo que el reinicio **no** rompe: `ListenActor` es un servidor HTTP pasivo, así que la cámara
vuelve a postear sus eventos sola cuando termina de arrancar. No hay nada que reconectar.

Verificado de punta a punta contra el DS-2XM6825G0: perfil ya alineado → no escribe; `GovLength`
distinto → `statusCode 1`, en caliente, sin reinicio; `SmartCodec` distinto dentro de la ventana
→ `statusCode 7`, reinicio, cámara de vuelta en 2 min con el ajuste efectivo, y el ciclo
siguiente idempotente.

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

- **latente** — un POST `multipart/form-data` no se parsea, y este modelo soporta envío de imagen
  (`isSupportPicture=true`). Hoy al menos deja WARN;
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

- `go test ./...` **falla**, y siempre por lo mismo:
  [peoplecounting/listenner_test.go](peoplecounting/listenner_test.go) es un esqueleto de `gotests`
  con `want: nil` sin completar. El parser funciona; el caso de prueba nunca se terminó de
  escribir. `./isapi/` sí pasa, así que compará antes y después en vez de leer el `FAIL` global
  como una regresión tuya.
- Código muerto conservado a propósito: `client/_business/` (copia previa de los actores),
  `client/pubsub-actor.go_`, `client/service/service.go_`.
- `client/comm/grpc`, `client/comm/pubsub` y `client/service` definen una interfaz de servicio
  que `client/main` **no usa**.
- `MapInterface`/`MyMap` en [client/map.go](client/map.go) no se usa; los mapas de `CountingActor`
  se acceden sin lock (correcto: siempre desde el hilo del actor).
- `ListenActor` no cierra el servidor HTTP salvo por `ctx.Done()`; el `close(ch)` en
  `Listen` corre incluso en shutdown limpio y termina en `msgListenError` → panic → restart.

**Corregido en 1.0.30, vale saber que existió**: `CountingActor` pedía el GPS con
`ctx.RequestFuture(ctx.Parent(), ...)` al manejar `*msgPingError`, pero es un actor de raíz y
`ctx.Parent()` es `nil` — pedirle a un PID nulo hace panic. Cada fallo del keep-alive de la cámara
reiniciaba el actor, replicaba la boltdb y republicaba acumulados recalculados (medido: 7 arranques
y `inputs0` yendo 2 → 3 → 1 → 3), mientras el evento `CounterDisconnected` nunca llegaba a
publicarse. Si aparecen contadores que retroceden en datos históricos, este es el sospechoso.
Ojo con el patrón: `ctx.Parent()` **sí** es correcto en los hijos (`EventActor`), no en `counting`.

## Documentación de referencia (`docs/`)

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
