# go-hikvision

Binario embebido que integra cámaras **Hikvision de conteo de personas** con la plataforma de
telemetría. Corre en el gateway del vehículo y hace cuatro cosas:

1. **Cuenta** los pasajeros que entran y salen por cada puerta, validando contra el estado de la
   puerta y persistiendo los acumulados para que sobrevivan a un reinicio.
2. **Publica** los conteos y los eventos por MQTT local.
3. **Extrae el video** de cada paso y lo deja en disco con un archivo de datos por evento, para
   que otro proceso los suba.
4. **Mantiene la configuración de las cámaras**: hora y NTP, horario de grabación, salud de la
   tarjeta SD y perfil de codificación.

Las tres últimas se habilitan por separado. Sin sus flags, el binario solo cuenta y publica.

```bash
./hikvision -version       # imprime la versión y termina con código 2 (no es un error)
```

---

## Requisitos

| | |
|---|---|
| Arquitectura | Linux ARMv7 (`uname -m` → `armv7l`) |
| Broker MQTT | `tcp://127.0.0.1:1883`, local, sin credenciales |
| Puerto de escucha | `8088` por defecto, alcanzable **desde la cámara** |
| Disco | `/SD` para la base de datos, los logs y los clips |

El binario es estático y sin cgo: no depende de la libc del equipo.

### La cámara tiene que apuntar al gateway

El binario **no consulta** los conteos: la cámara los empuja por HTTP. Si el destino de
notificación de la cámara no tiene la IP del gateway, no llega ningún evento y el binario se ve
perfectamente sano.

Se verifica pidiéndole a la cámara que pruebe el destino. Responde `errorCode 0` / `ok` si
alcanza al gateway, o `errorCode 151` / `connect server fail` si no:

```bash
curl -s --digest -u admin:CLAVE -X POST \
  --data-binary @destino.xml -H 'Content-Type: application/xml' \
  "http://IP_CAMARA/ISAPI/Event/notification/httpHosts/1/test"
```

La cámara admite varios destinos (`/httpHosts/1`, `/2`, `/3`), así que se puede agregar el gateway
sin borrar una configuración existente.

---

## Credenciales

Las credenciales de la cámara van en la variable de entorno `HIKVISION_CREDENTIALS`, cifradas. El
valor lo genera el propio binario, leyendo usuario y clave por entrada estándar para que no queden
en la lista de procesos:

```bash
printf 'admin\nLA-CLAVE\n' | ./hikvision -encryptCredentials
# HIKVISION_CREDENTIALS=kCUGnjpq...
```

Ese valor se pone en el unit file o el script de arranque.

> **Esto es ofuscación, no un secreto.** La llave está embebida en el binario, así que quien lo
> tenga puede recuperar las credenciales. Lo que evita es que la clave quede legible en el unit
> file, el script de despliegue o un volcado de logs. No lo presentes como un control de
> seguridad.

Sin la variable el binario arranca igual y solo deshabilita el diálogo con la cámara: el conteo
sigue funcionando.

---

## Flags

### Básicos

| Flag | Default | |
|---|---|---|
| `-socket` | `:8088` | puerto donde la cámara publica los eventos |
| `-pathdb` | `/SD/boltdbs/countingdb` | base de datos de los acumulados |
| `-debug` | off | habilita el nivel de log `BUILD` |
| `-logStd` | off | log a stderr en vez de syslog |
| `-logxml` | off | guarda el XML recibido en `/SD/logs/camera*` |
| `-version` | | imprime la versión y termina |
| `-encryptCredentials` | | genera el valor de `HIKVISION_CREDENTIALS` |

### Conteo

**`-camera`** asocia cada cámara con su puerta. Dos formas, y la explícita es la recomendada:

```bash
-camera 192.168.188.21:1                  # el id de puerta va escrito
-camera 10.0.0.1:0 -camera 10.0.0.2:1     # dos puertas, el orden no importa
-camera 10.0.0.1 -camera 10.0.0.2         # posicional: la n-ésima aparición es la puerta n-1
```

No arranca si se mezclan las dos formas, si el id está fuera de 0..15, o si se repite un id.
Detenerse al arrancar es preferible a contar la puerta equivocada durante un turno: los
contadores publicados quedarían mal y no hay forma de repararlos después.

Sin `-camera`, la puerta se deriva de la IP de origen con la regla histórica:
`192.168.188.21` → puerta 1, cualquier otra → puerta 0.

**`-zeroOpenState`** indica si el estado "puerta abierta" llega como `0`. **`-countWithCloseDoor`**
permite contar con la puerta cerrada. Los dos son repetibles:

```bash
-zeroOpenState=true                        # aplica a TODAS las puertas
-zeroOpenState=false -zeroOpenState=true   # posicional: puerta 0 y puerta 1
```

Una sola aparición cubre todas las puertas, porque describe cómo está cableado el vehículo y eso
no cambia entre la puerta delantera y la trasera. Dos o más apariciones son posicionales, que es
la forma de darle un valor distinto a cada puerta.

> `-countWithCloseDoor` **desactiva la validación contra el estado de la puerta**. Solo tiene
> sentido en un banco de pruebas sin sistema de puertas. En un vehículo hace que el conteo ignore
> las puertas.

### Extracción de video

Se habilita con `-videoDir`, y exige además `-camera` y credenciales. Si falta alguna de las tres,
el actor no se crea y el conteo sigue igual.

| Flag | Default | |
|---|---|---|
| `-videoDir` | vacío | directorio de los clips; vacío **deshabilita** la extracción |
| `-videoPreRoll` | `10s` | cuánto video guardar antes del evento |
| `-videoDuration` | `16s` | duración del clip |
| `-videoQueue` | `500` | extracciones pendientes máximas |

Los 10 segundos de pre-roll no son arbitrarios: el `dateTime` del evento llega entre **2 y 7
segundos después** del cruce físico, y el retraso varía. Con menos, el cruce queda en el borde del
clip o afuera.

### Mantenimiento de cámara

Se habilita con `-ntpServer`, y exige además `-camera` y credenciales.

| Flag | Default | |
|---|---|---|
| `-ntpServer` | vacío | servidor NTP; vacío **deshabilita** todo el bloque |
| `-ntpPort` | `123` | |
| `-ntpInterval` | `1h` | cada cuánto sincroniza la cámara |
| `-timeZone` | `CST+5:00:00` | zona POSIX; **el signo está invertido**, esto es UTC−5 |
| `-timeDriftMax` | `10s` | deriva tolerada antes de alarmar |
| `-cameraCheckInterval` | `30m` | cada cuánto se revisa cada cámara |
| `-recordStart` | `04:00:00` | inicio de la ventana de grabación |
| `-recordEnd` | `23:59:00` | fin de la ventana |

El binario **corrige la configuración de hora pero nunca escribe el reloj** de la cámara. Un salto
de reloj hacia atrás hace que se descarten eventos, así que ante una deriva persistente alarma y
deja que la corrija el NTP de la cámara.

Un evento fuera de la ventana de grabación no tiene video posible. Y no conviene poner 24/7 sin
pensarlo: con 507 kbps, una SD de 8 GB da **1.4 días** de retención grabando todo el día contra
**1.7 días** grabando veinte horas. La palanca real de la retención es el tamaño de la tarjeta.

El almacenamiento solo se **reporta**, nunca se formatea. Una SD en estado `unformatted` hace que
ningún clip se pueda extraer.

### Perfil de codificación

Todos vacíos o en cero dejan la cámara como esté.

| Flag | Default | |
|---|---|---|
| `-smartCodec` | vacío | `off` desactiva H.264+, `on` lo activa |
| `-videoCodec` | vacío | `h264` o `h265` |
| `-videoFrameRate` | `0` | cuadros por segundo del stream principal |
| `-videoGop` | `0` | largo del GOP en cuadros |
| `-videoQuality` | `0` | calidad VBR, 1..100 (el modelo verificado admite 1,20,40,60,80,100) |
| `-videoBitrateMax` | `0` | techo de bitrate en kbps, 32..16384 |
| `-encoderMinUptime` | `30m` | cuánto debe llevar encendido el binario antes de tocar el perfil |

**`-encoderMinUptime` es el único freno contra un bucle de reinicios de cámara.** El tope de "un
reinicio por cámara" vive en memoria y se pierde al arrancar de nuevo, así que un binario que
reinicia en bucle solo se detiene si nunca alcanza este umbral. En `0` el perfil se aplica en el
primer ciclo, que sirve para probar; por debajo de 5 minutos el binario deja un WARN al arrancar.

Cualquiera de estos flags habilita el bloque de mantenimiento por sí solo: **no hace falta
`-ntpServer`**. Sin él no se toca el reloj de la cámara, pero el horario de grabación sí se alinea.

`-videoQuality` es la palanca que baja el tamaño. `-videoBitrateMax` solo acota el peor caso: en
la cámara verificada viene en 8192 kbps mientras graba a ~200, así que no ata nada.

Un valor mal escrito en `-smartCodec` o `-videoCodec` **detiene el arranque**, en vez de quedar
ignorado y dejar la cámara con el ajuste viejo.

### La combinación recomendada

```
-videoCodec h265 -smartCodec off -videoFrameRate 12
```

Medido en la misma cámara, misma escena, ventana de 12 s:

| codec | H.264+ | fps | hueco máx | **KB/s** | ¿se ve el cruce? |
|---|---|---|---|---|---|
| H.264 | on | 20 | 0.08 s | 25.9 | **a veces** |
| H.265 | on | 20 | **10.2 s** | 2.9 | no |
| H.265 | off | 20 | 0.18 s | 28.2 | sí |
| H.264 | off | 20 | 0.08 s | 42.9 | sí |
| **H.265** | **off** | **12** | 0.18 s | **16.7** | **sí** |

El extractor maneja los dos codecs y elige según lo que la cámara ofrezca para cada tramo, así
que las grabaciones anteriores a un cambio de codec siguen siendo extraíbles. Cada clip anota el
suyo en el sidecar (`codec`).

**`-smartCodec off` es obligatorio si se quiere video útil.** Con H.264+ activo la cámara deja de
emitir cuadros cuando el cambio en la escena le parece pequeño, y una persona atravesando una
puerta ocupa una fracción chica del cuadro. Medido con tráfico real de personas: **18 de 23 clips
salieron congelados**, con huecos de 4 a 14.5 segundos sin una sola imagen nueva.

El ahorro de H.264+ es exactamente lo que no graba — 538 KB por clip congelado contra 899 KB por
clip fluido — así que no se puede tener las dos cosas.

**Aplicarlo reinicia la cámara una vez.** `SmartCodec` es un campo que la cámara guarda pero deja
inerte hasta reiniciar, así que el binario escribe y reinicia en el mismo ciclo. Eso corta unos
**70 segundos de grabación** y la cámara **no envía eventos durante ~2 minutos** mientras arranca,
así que los pasajeros que cruzan en esa ventana no se cuentan. Ocurre **una sola vez por cámara**:
en estado estable el perfil coincide y no se escribe nada.

El binario espera 30 minutos encendido antes de tocar el perfil, para que un equipo que reinicia en
bucle no reinicie cámaras.

`-videoFrameRate` y `-videoGop` se aplican en caliente, sin reiniciar. Un valor que el modelo no
admita se guarda recortado **respondiendo OK**, así que el binario relee después de escribir y, si
el valor no quedó, deja un ERROR y no vuelve a intentarlo hasta el próximo arranque.

---

## Despliegue típico

Vehículo con una sola cámara, en la puerta trasera:

```bash
export HIKVISION_CREDENTIALS=...

./hikvision \
  -socket :8088 -pathdb /SD/boltdbs/countingdb \
  -camera 192.168.188.21:1 \
  -zeroOpenState=true \
  -videoDir /SD/video \
  -ntpServer ntp2.inm.gov.co \
  -smartCodec off
```

Dos cámaras:

```bash
./hikvision \
  -socket :8088 -pathdb /SD/boltdbs/countingdb \
  -camera 192.168.188.21:0 -camera 192.168.188.22:1 \
  -zeroOpenState=false -zeroOpenState=true \
  -videoDir /SD/video \
  -ntpServer ntp2.inm.gov.co \
  -smartCodec off
```

---

## Verificación

### 1. El binario corre en el equipo

```bash
uname -m                  # armv7l
./hikvision -version      # imprime la versión, termina con código 2
```

### 2. El banner de arranque dice lo que se espera

Con `-logStd`, las primeras líneas resumen la configuración interpretada. **Es el punto de control
más importante**, porque muestra a qué puerta quedó asignada cada cosa:

```
zeroOpenState: true (todas las puertas)
countWithCloseDoor: false (todas las puertas)
cameras: puerta 1 -> 192.168.188.21
[ info ] video extraction enabled: dir="/SD/video" preRoll=10s duration=16s queue=500
[ info ] camera time maintenance enabled: ntp=ntp2.inm.gov.co:123 every 1h0m0s, ...
[ info ] back camera counter START --  version: ...
```

Si `cameras` dice `puerta 0` cuando se esperaba `puerta 1`, los contadores van a publicarse en el
campo equivocado.

### 3. Llegan los eventos de la cámara

Hacer pasar una persona y buscar el conteo:

```bash
grep COUNTERSDOOR /var/log/syslog | tail -5
```

```json
{"type":"COUNTERSDOOR","value":{"coord":"$GPRMC,...","id":1,"state":1,
 "counters":[1,0],"type":"CAMERA","event_id":"9668c0b7"}}
```

`counters` son `[entradas, salidas]` **de ese evento**, no acumulados. Puede valer más de 1 cuando
dos personas cruzan casi juntas: en ese caso un solo `event_id` representa varios pasajeros.

Si no aparece nada, el problema está en el camino cámara → gateway. Verificar el destino de
notificación de la cámara (ver arriba).

### 4. El estado de la puerta es el correcto

```bash
grep "counting inputs when door" /var/log/syslog | tail -20
```

Este mensaje aparece cuando llega un conteo con la puerta cerrada. **Repetido y con cruces
reales**, significa que `-zeroOpenState` está invertido para esa puerta, o que nunca llega el
estado de la puerta por MQTT. En los dos casos se están descartando pasajeros.

### 5. Los clips salen y se ven fluidos

```bash
ls /SD/video/$(date +%Y-%m-%d)/
grep -h max_gap_s /SD/video/*/*.json
```

`max_gap_s` es el hueco más largo entre dos imágenes del clip. A 20 cuadros por segundo lo normal
es **0.05 a 0.08**. Un valor de segundos significa que el clip se ve congelado en ese tramo, y el
binario deja el aviso en el log:

```
[ warn ] video 20260821T094313_NE-RCX-0000_p0_e2e16bf5.mp4: hueco de 6.125s sin fotos
(normal a 20 fps: 0.05s). El clip se va a ver congelado en ese tramo. Causa medida:
H.264+ activo en la cámara; se apaga con -smartCodec off
```

Una ráfaga de pasos produce **menos clips que eventos**: los que caen en la misma ventana de
tiempo comparten el clip, y cada uno tiene su propio archivo de datos. Con 50 eventos seguidos se
obtienen unos 25 clips.

La extracción va **a tiempo real**: un clip de 16 segundos tarda 16 segundos en extraerse. Después
de una ráfaga, los archivos siguen apareciendo durante un par de minutos.

### 6. El acumulado publicado es coherente

```bash
mosquitto_sub -h 127.0.0.1 -t 'COUNTERSMAPDOOR' -C 1
```

```json
{"inputs0":0,"inputs1":123,"outputs0":0,"outputs1":118,
 "anomalies0":0,"anomalies1":0,"tampering0":0,"tampering1":0}
```

Se publica cada 45 segundos. **No se publica si entradas y salidas suman 0**, para no pisar
contadores válidos en la plataforma mientras la base persistida se recupera. `anomalies*` está
reservado y siempre va en `0`.

### 7. El mantenimiento de cámara corrió

```bash
mosquitto_sub -h 127.0.0.1 -t 'EVENTS/counterevents'
```

El primer ciclo ocurre 90 segundos después del arranque y luego cada `-cameraCheckInterval`. Solo
publica **cuando algo cambia de estado**, no en cada ciclo. Con `-debug` el resultado de cada ciclo
queda en el log aunque no haya cambios.

---

## Salidas

### MQTT

| Tópico | Cuándo | Mensajes |
|---|---|---|
| `COUNTERSMAPDOOR` | cada 45 s | acumulados por puerta |
| `EVENTS/backcounter` | por cada paso y al perder la cámara | `COUNTERSDOOR`, `CounterDisconnected` |
| `EVENTS/counterevents` | tamper, clip escrito, mantenimiento de cámara | `TAMPERING`, `COUNTERSDOORVIDEO`, `CAMERATIME` |

`QoS 0`, sin retain.

### Archivos de video

```
/SD/video/2026-08-21/20260821T094306_NE-RCX-0000_p0_11bbd7e1.mp4        ← el clip
/SD/video/2026-08-21/20260821T094306_NE-RCX-0000_p0_in_11bbd7e1.json    ← entrada
/SD/video/2026-08-21/20260821T094308_NE-RCX-0000_p0_out_79322baa.json   ← salida, mismo clip
```

El nombre lleva fecha y hora locales, el **hostname del gateway**, la puerta, el tipo (solo en el
archivo de datos) y el identificador del evento. Es autosuficiente a propósito: en el repositorio
final la estructura de directorios se pierde, y así se puede buscar por fecha, por equipo, por
puerta o por evento sobre un directorio plano.

El `.mp4` no lleva el sufijo `in`/`out` porque es de una puerta y una ventana de tiempo, y puede
contener entradas y salidas a la vez.

El archivo de datos de cada evento:

```json
{
  "event_id": "11bbd7e1",
  "gateway_hostname": "NE-RCX-0000",
  "door": 0,
  "type": "in",
  "event_time": "2026-08-21T09:43:06-05:00",
  "event_time_ms": 1787323386000,
  "clip": "20260821T094306_NE-RCX-0000_p0_11bbd7e1.mp4",
  "clip_start_requested": "2026-08-21T09:42:56-05:00",
  "offset_s": 10,
  "duration_s": 16,
  "camera": "192.168.188.21",
  "camera_serial": "DS-2XM6825G0/C-IVS20221126AAWRK97545100",
  "camera_mac": "bc:9b:5e:e7:ef:05",
  "camera_counter": 1,
  "samples": 321,
  "bytes": 875379,
  "max_gap_s": 0.08,
  "fps_effective": 20.06
}
```

- **`event_time_ms` va en milisegundos pero la resolución de origen es de un segundo**: la cámara
  envía el `dateTime` sin fracción, así que siempre termina en `000`.
- **`clip_start_requested` es el instante pedido, no el primer cuadro.** La cámara posiciona la
  reproducción en el cuadro completo anterior, así que el video puede empezar hasta un GOP antes.
  Da más pre-roll que el configurado, nunca menos. Lo mismo vale para `offset_s`.
- **`camera_counter` es el incremento de este evento**, no el acumulado. **Puede ser mayor a 1**,
  y entonces el clip no siempre muestra a todos: la cámara manda acumulados, así que si el binario
  estuvo un rato sin recibir eventos —recién arrancado, o la cámara sin alcanzarlo— ese incremento
  junta cruces repartidos en minutos y el clip cubre solo el último. El conteo es correcto en los
  dos casos; cuando el video no alcanza a respaldarlo el binario deja un WARN:

  ```
  el evento de la puerta 1 trae 8 pasajeros pero el clip arranca en 11:39:50: los cruces
  anteriores a ese instante no quedaron grabados. El conteo es correcto, el video cubre
  solo el final
  ```

  Cuando los pasajeros cruzan juntos —dos personas con segundos de diferencia— entran los dos en
  la ventana y no hay aviso.
- **`max_gap_s` y `fps_effective`** permiten juzgar si el clip sirve sin abrirlo.

El clip y cada archivo de datos se escriben como `.part` y se **renombran al terminar**. El rename
es atómico, así que un proceso que recorra el directorio nunca ve un archivo a medio escribir y no
hace falta coordinar.

Este binario **no borra clips** — de eso se encarga el proceso que los suba — pero **deja de
extraer** cuando el espacio libre baja de 512 MB, porque en la misma partición vive la base de
datos del conteo.

---

## Síntomas y causas

| Síntoma | Causa probable | Verificación |
|---|---|---|
| No llega ningún conteo | la cámara no alcanza al gateway | prueba del destino de notificación en la cámara |
| `counting inputs when door ... is closed` repetido | `-zeroOpenState` invertido, o no llega el estado de puerta por MQTT | `mosquitto_sub -t 'EVENTS/doors'` |
| Los contadores aparecen en la puerta equivocada | `-camera` sin el `:id`, o la IP de origen no se preserva | banner de arranque, línea `cameras:` |
| Todas las puertas cuentan como una | los eventos llegan por NAT o reenvío de puerto: el origen es el router | comparar la IP de origen con la de la cámara |
| No se extrae ningún clip | falta `-videoDir`, `-camera` o credenciales | banner de arranque |
| `sin grabación para ... N evento(s) sin video` | el evento cae fuera de la ventana de grabación de la cámara | `-recordStart` / `-recordEnd` |
| Clips congelados, `max_gap_s` en segundos | H.264+ activo en la cámara | `-smartCodec off` |
| `almacenamiento en "unformatted"` | la SD de la cámara no sirve para grabar | formatear la cámara a mano; el binario no lo hace |
| `rechazó las credenciales` | `HIKVISION_CREDENTIALS` incorrecta | regenerar el valor y reiniciar el binario |
| `NO quedó guardado` | el valor pedido no está entre los que admite el modelo | `GET /ISAPI/Streaming/channels/101/capabilities` |

Un `401` de la cámara **apaga el diálogo con esa cámara hasta el próximo arranque**: la cámara
bloquea el usuario tras varios fallos, así que insistir cada ciclo con una credencial mala la
dejaría inaccesible en campo.

---

## Compilación

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -ldflags="-s -w" -o hikvision-<version>-armv7 ./client/main
```

El `go.mod` resuelve tres módulos desde el filesystem (`../go-doors`, `../go-actors`,
`../../asynkron/protoactor-go`), así que el repositorio tiene que estar en
`$GOPATH/src/github.com/dumacp/go-hikvision` con los repositorios hermanos presentes.

Conviene nombrar el artefacto con la versión y el hash del commit: la versión sola no identifica un
binario, porque varios commits pueden compartirla.
