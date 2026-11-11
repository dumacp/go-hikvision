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
5. **Apaga la cámara con el vehículo**, cortándole la energía por una GPIO cuando se apaga la
   ignición, y la vuelve a encender al arrancar.

Y se puede **configurar desde la plataforma** (atributo `DEV_CAMERACOUNT_CONFIG`), sin entrar
al equipo a editar el lanzador.

Las funciones 3 a 5 se habilitan por separado. Sin sus flags, el binario solo cuenta y publica.

```bash
./hikvision -version       # imprime la versión y termina con código 2 (no es un error)
```

---

## Cómo funciona

Es un único proceso Go, escrito sobre el modelo de **actores** de
[protoactor-go](https://github.com/asynkron/protoactor-go): cada responsabilidad es un actor
con su propio buzón, y un actor que falla se reinicia solo sin tumbar a los demás.

```
cámara --HTTP POST (XML o multipart)--> :8088  ListenActor   (recibe y clasifica por puerta)
                                                    |
                                                    v
                                           CountingActor     (valida contra la puerta, acumula,
                                            |  persiste en boltdb, publica por MQTT)
           +----------+----------+----------+----------+----------+
         events     doors      ping       video      camera    ignition
       (arma JSON) (estado   (keep-alive) (clips)  (hora, NTP, (GPIO de la
                   de puerta)                       SD, codec)  cámara)

plataforma --NATS--> nats-gwIot --protoactor remote 127.0.0.1:8090--> parametros (configuración)
```

- **El conteo no depende de nada más.** Video, mantenimiento, apagado y configuración remota son
  accesorios: si fallan, el binario sigue contando y lo deja en el log.
- **La cámara empuja los eventos**; el binario nunca los consulta.
- **El video y la cámara se manejan en Go puro**, sin `ffmpeg` ni librerías de C: el binario es
  un único ejecutable estático. Habla ISAPI (HTTP + XML con Digest) y RTSP, y lee el formato de
  grabación de Hikvision (MPEG-PS) por su cuenta.

El detalle técnico —cada decisión de diseño con la medición que la justifica— está en
[`CLAUDE.md`](CLAUDE.md). Los contratos con la plataforma (MQTT, archivos de video,
configuración remota) están en [`docs/contrato/`](docs/contrato/).

---

## Requisitos

| | |
|---|---|
| Arquitectura | Linux ARMv7 (`uname -m` → `armv7l`); verificado en i.MX6, kernel 4.1.15, BusyBox 1.23 |
| Binario | estático, sin cgo, ~18 MB; no depende de la libc del equipo |
| Cámaras | **DS-2XM6825G0** (cualquier firmware) y **DS-2XM6825G1 con firmware V4.2.2 o posterior**. La G1 en V3.2.2 se rechaza con un error que pide actualizarla |
| Broker MQTT | `tcp://127.0.0.1:1883`, local, sin credenciales |
| Puerto de eventos | `8088` por defecto (`-socket`), alcanzable **desde la cámara** y **sin NAT**: la puerta se deduce de la IP de origen |
| Puerto local | `127.0.0.1:8090` (o el siguiente libre) para la configuración de plataforma; no se expone afuera |
| Procesos vecinos | `nats-gwIot` para recibir configuración de la plataforma; `go-ignition` (`EVENTS/ignition`) para el apagado de la cámara; el sistema de puertas (`EVENTS/doors`) para validar el conteo |
| Disco | `/SD` para la base de datos, la configuración en cache, los logs y los clips. La extracción se detiene por debajo de `-videoMinFree` (512 MiB) |
| Credenciales | usuario de la cámara con permiso de configuración, en `HIKVISION_CREDENTIALS` (ver abajo) |
| Compilar | Go 1.25 y el clon local de `protoactor-go` (ver "Compilación") |

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
| `-withoutEventID` | off | omite `event_id` del `COUNTERSDOOR`; compatibilidad temporal |

> **`-withoutEventID` es una compuerta temporal, no una opción de producto.** Devuelve el
> `COUNTERSDOOR` al formato anterior a 1.0.32, para plataformas que todavía no saben procesar el
> campo `event_id`. Mientras esté activo, la plataforma **no puede cruzar el paso con su video**:
> `event_id` es justamente la llave de ese cruce. Se quita el día que la plataforma acepte el
> campo; entonces el binario corre sin el flag y no hace falta recompilar.
>
> El binario deja un WARN en cada arranque mientras el flag esté puesto.

El binario tiene **43 flags**, agrupados abajo por función. La columna **plataforma** indica los
que también se pueden fijar desde `DEV_CAMERACOUNT_CONFIG` (ver "Configuración desde la
plataforma"); un flag escrito en la línea de comandos **siempre le gana** a la plataforma.

### Utilitarios (hacen una cosa y terminan, sin contar)

| Flag | |
|---|---|
| `-version` | imprime la versión |
| `-encryptCredentials` | lee usuario y clave por stdin e imprime el valor de `HIKVISION_CREDENTIALS` |
| `-cameraPowerSet 0\|1` | pone la GPIO de `-cameraPowerGPIO` en ese nivel y termina. Sirve para averiguar contra el vehículo real qué nivel apaga la cámara |
| `-gpioRegisters N` | lee los registros del SoC (GDIR, DR, PSR) de la línea N y termina. Solo lee. Para diagnosticar una GPIO cuyo `value` de sysfs no refleja lo escrito |

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

| Flag | Default | plataforma | |
|---|---|---|---|
| `-camera` | (regla histórica) | `cameras[].ip` / `door` | IP de cada cámara y su puerta |
| `-zeroOpenState` | `false` | `cameras[].zeroOpenState` | el estado "abierta" llega como `0` |
| `-countWithCloseDoor` | `false` | `cameras[].countWithCloseDoor` | contar aunque la puerta esté cerrada |

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

| Flag | Default | plataforma | |
|---|---|---|---|
| `-videoDir` | vacío | `videoDir` | directorio de los clips; vacío **deshabilita** la extracción |
| `-videoEnabled` | `true` | `videoEnabled` | interruptor de la extracción; es el que la plataforma puede apagar a distancia |
| `-videoPreRoll` | `10s` | — | video **antes del primer paso** del clip |
| `-videoDuration` | `16s` | — | clip de **un paso solo**: el pre-roll antes y el resto (6 s) después |
| `-videoBurstGap` | `10s` | `videoBurstGap` (s) | un paso que llega a lo sumo este tiempo después del anterior, en la misma puerta, va al **mismo** clip, que crece. `0` = un clip fijo por paso |
| `-videoMaxDuration` | `60s` | `videoMaxDuration` (s) | tope del clip; el paso que lo superaría abre otro |
| `-videoQueue` | `500` | — | extracciones pendientes máximas |
| `-videoMinFree` | `512` | — | piso de espacio libre en MiB; bajo él se deja de extraer. `0` lo deshabilita |

Cómo se arma un clip:

```
       |<- -videoPreRoll ->|<--- pasos de la ráfaga --->|<- 6 s ->|
     inicio             paso 1   paso 2 ... paso N              fin
                           |<=10s=>|  cada paso a ≤ -videoBurstGap del anterior
     duración total ≤ -videoMaxDuration
```

| pasos | clips |
|---|---|
| uno a las 10:00:00 | 09:59:50–10:00:06 (16 s) |
| 10:00:00, :06 y :12 | **uno**, 09:59:50–10:00:18 (28 s) |
| 10:00:00 y 10:00:15 | dos de 16 s: los separan más que `-videoBurstGap` |
| gente subiendo cada 5 s durante 2 min | clips de hasta 60 s, uno detrás de otro |

Los 10 segundos de pre-roll no son arbitrarios: el `dateTime` del evento llega **después** del
cruce físico —medido entre 2 y 7 s en la G0 y entre 0 y 2.3 s en la G1— y el retraso varía. Con
menos, el cruce queda en el borde del clip o afuera.

**Las dos palancas de datos móviles son distintas**: `-videoMaxDuration` acota el tamaño de cada
archivo (~25 KB/s, así que 60 s son ~1.5 MB) y `-videoBurstGap` decide cuánto tiempo sin nadie
entre pasos queda dentro de un clip. Ninguna acota el consumo por hora.

**Cómo baja el video, según la cámara.** El binario prueba primero la **descarga HTTP** del
archivo grabado y, si la cámara no la ofrece, usa **RTSP**. Lo aprende solo, por puerta:

| | G0 | G1 (V4.2.2) |
|---|---|---|
| vía | RTSP | descarga HTTP |
| tiempo por clip | a tiempo real: 16 s de video tardan 16 s | 1 a 8 s, según dónde caiga la ventana en el archivo |
| espera antes de poder bajarlo | ~5 s | hasta 3 min: la cámara tarda en indexar lo que graba |

En la G1 la reproducción por RTSP **está rota** (entrega un video verde, el mismo para cualquier
hora pedida), y por eso se usa la descarga.

### Mantenimiento de cámara

Se habilita con `-ntpServer`, y exige además `-camera` y credenciales.

| Flag | Default | |
|---|---|---|
| `-ntpServer` | vacío | servidor NTP; vacío **deshabilita** todo el bloque. También desde la plataforma (`ntpServer`) |
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

El horario de grabación **no se toca si la cámara ya graba de más**: la G1 viene con
`03:15–24:00`, que cubre el `04:00–23:59` por defecto. Y si la cámara responde OK a un cambio
pero no lo guarda —la G1 hace eso con el horario—, el binario lo detecta al releer, avisa una vez
y no insiste.

### Perfil de codificación

Todos vacíos o en cero dejan la cámara como esté.

| Flag | Default | |
|---|---|---|
| `-smartCodec` | vacío | `off` desactiva H.264+, `on` lo activa. También desde la plataforma (`smartCodec`) |
| `-videoCodec` | vacío | `h264` o `h265`. También desde la plataforma (`videoCodec`) |
| `-videoFrameRate` | `0` | cuadros por segundo del stream principal. También desde la plataforma (`videoFrameRate`) |
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

### Apagado de la cámara con el vehículo

Se habilita con `-cameraPowerGPIO`. Escucha `EVENTS/ignition` de `go-ignition` y actúa solo sobre
`SUSPEND_EVT`, que ya viene con su período de gracia.

| Flag | Default | plataforma | |
|---|---|---|---|
| `-cameraPowerGPIO` | `0` | `cameraPowerGPIO` | línea de sysfs que corta la energía de la cámara; `0` deshabilita |
| `-cameraPowerOffLevel` | `1` | `cameraPowerOffLevel` | nivel que significa APAGAR; el otro es "encendida" |
| `-cameraPowerOffDelay` | `20s` | — | espera tras la señal de apagado, para que termine el clip en curso |
| `-cameraBootDelay` | `2m` | — | cuánto tarda la cámara en arrancar; mientras tanto no se le habla |

En los gateways del banco la línea es la **`165`** y el nivel de apagado es **`1`**. Antes de
fijar el flag en un vehículo nuevo, comprobar la polaridad con `-cameraPowerSet`: si queda al
revés, el binario deja la cámara sin energía al arrancar.

Si el número de GPIO viene de un **flag** y no se puede tomar la línea, el binario no arranca. Si
viene de la **plataforma**, se deshabilita solo el apagado y el conteo sigue.

### Configuración desde la plataforma

El binario descarga el atributo **`DEV_CAMERACOUNT_CONFIG`** del mapa de parámetros del equipo,
a través de `nats-gwIot`, y lo aplica **en caliente**, sin reiniciar y sin tocar los acumulados.

| Flag | Default | |
|---|---|---|
| `-id` | hostname del gateway | llave del equipo en el mapa de parámetros |
| `-platformConfigCache` | `/SD/boltdbs/cameracount-config.json` | última configuración válida, para que un arranque sin red la conserve; vacío lo deshabilita |
| `-disablePlatformConfig` | off | no descarga nada: el binario se comporta como antes de que esto existiera |

El valor del atributo es un **string que contiene JSON**:

```json
{
  "cameras": [
    {"ip": "192.168.186.93", "door": 0, "zeroOpenState": true, "countWithCloseDoor": true},
    {"ip": "192.168.186.91", "door": 1, "zeroOpenState": true, "countWithCloseDoor": false}
  ],
  "ntpServer": "ntp2.inm.gov.co",
  "videoCodec": "h265", "smartCodec": "off", "videoFrameRate": 12,
  "videoEnabled": true, "videoDir": "/SD/video265/",
  "videoBurstGap": 10, "videoMaxDuration": 60,
  "cameraPowerGPIO": 165, "cameraPowerOffLevel": 1
}
```

Tres reglas que conviene tener presentes:

1. **El flag de la línea de comandos gana.** Si el lanzador pasa `-videoFrameRate`, la plataforma
   no lo puede cambiar. Para delegarle un parámetro hay que **quitar el flag del lanzador**. El
   banner de arranque dice, parámetro por parámetro, de dónde salió cada valor (`[flag]` o
   `[plataforma/default]`).
2. **Un campo ausente no cambia nada**, pero `cameras` reemplaza la lista **entera**: mandar una
   sola cámara deja la otra puerta sin cámara.
3. **Un JSON inválido se rechaza completo** y el equipo sigue con la configuración que tenía.

El contrato completo está en
[`docs/contrato/configuracion-plataforma.md`](docs/contrato/configuracion-plataforma.md).

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

Dos cámaras, con el perfil recomendado y el apagado por GPIO (el lanzador del banco
FLO-W7-0138):

```bash
./hikvision \
  -camera 192.168.186.93:0 -camera 192.168.186.91:1 \
  -videoDir /SD/video265/ \
  -countWithCloseDoor true -countWithCloseDoor false \
  -zeroOpenState=true -zeroOpenState=true \
  -videoCodec h265 -smartCodec off -videoFrameRate 12 \
  -ntpServer ntp2.inm.gov.co \
  -cameraPowerGPIO 165 -cameraPowerOffLevel 1
```

Para que la plataforma pueda cambiar un parámetro, hay que sacarlo de esta línea.

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
[ info ] configuración efectiva al arrancar:
  puerta 0             192.168.186.93     [flag]  zeroOpenState=true  [flag]  countWithCloseDoor=true  [flag]
  puerta 1             192.168.186.91     [flag]  zeroOpenState=true  [flag]  countWithCloseDoor=false [flag]
  ntpServer            ntp2.inm.gov.co    [flag]
  videoBurstGap        10s                [plataforma/default]
  ...
[ info ] video extraction enabled: dir="/SD/video265/" preRoll=10s duration=16s burstGap=10s maxDuration=1m0s ...
[ info ] back camera counter START --  version: 1.0.43
```

Si una cámara aparece en la puerta equivocada, los contadores van a publicarse en el campo
equivocado. `[flag]` marca los parámetros que la plataforma **no** puede cambiar.

En los gateways del banco el syslog va a **`/SD/var/log/messages`**, no a `/var/log/syslog`.

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

Una ráfaga de pasos produce **un clip por ráfaga**, con un archivo de datos por paso: medido en
el banco, 6 pasos en 25 s dieron **un clip de 43 s** y 6 archivos de datos.

Los clips **no aparecen enseguida**. En la G1 la cámara tarda hasta ~2 minutos en indexar lo que
graba, así que un clip suele estar listo **1 a 2 minutos** después del último paso de la
ráfaga. En la G0, por RTSP, la extracción va a tiempo real: un clip de 16 s tarda 16 s.

Y la única prueba de que un clip sirve es **mirarlo**: `max_gap_s` normal no garantiza que la
imagen se vea.

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
- **`clip_start_requested` es el instante pedido, no el primer cuadro.** El clip arranca en el
  cuadro completo anterior, así que el video puede empezar hasta un GOP antes (2 s en la G0, 4 s
  en la G1). Da más pre-roll que el configurado, nunca menos.
- **`offset_s` es el segundo del clip en que llegó el evento.** En los clips bajados por descarga
  (la G1) es **exacto**; por RTSP (la G0) puede quedarse corto hasta un GOP. El cruce físico se ve
  un poco **antes** del `offset_s`, porque la cámara manda el evento después del cruce.
- **`duration_s` es la duración pedida de ESE clip**, no un valor fijo: 16 s para un paso solo,
  hasta 60 s para una ráfaga.
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

### Qué pasa cuando el disco se llena

Este binario **no borra clips** — de eso se encarga el proceso que los suba — pero **deja de
extraer** cuando el espacio libre baja del piso de `-videoMinFree` (512 MiB por defecto), porque en
la misma partición vive la base de datos del conteo.

**No sobrescribe, no rota y no borra nada.** Aborta la extracción del paso en curso y sigue. El
video de ese pasajero se pierde; no hay reintento. El conteo, el MQTT y todo lo demás siguen
normales.

Es deliberado: este binario **no sabe cuáles clips ya se subieron** — eso lo sabe el proceso que
los sube. Borrar "el más viejo" para hacer sitio sería borrar justamente el que lleva más tiempo
esperando, el que con más probabilidad nunca llegó al repositorio. Prefiere perder el video que
todavía no existe antes que uno ya grabado. Y la prioridad real es la base del conteo: quedarse sin
video cuesta un clip, quedarse sin disco cuesta el acumulado del vehículo entero.

Se avisa **una sola vez** por episodio, no una vez por pasajero — con el disco al tope fallan todas
las extracciones, y un mensaje por evento serían cientos de líneas diarias idénticas:

```
[ warn ] espacio libre en /SD/video por debajo del piso: 480 MB contra 512 MB. Se DEJA de
         extraer video; el conteo y el MQTT siguen normales. No se borra ni se sobrescribe
         ningún clip...
[ info ] espacio libre en /SD/video recuperado: 900 MB, se vuelve a extraer video
```

Con `-ntpServer` o algún flag de codec, el espacio libre viaja además en cada evento `CAMERATIME`
como `video_dir_free_mb`, y el cruce del piso publica un evento propio (`videodir` /
`videodir_ok`). Sirve para ver venir el problema con semanas de anticipación, que es cuando todavía
se puede hacer algo.

> `-videoMinFree 0` desactiva el piso. El binario avisa al arrancar, porque entonces llena la
> partición hasta el último byte y ahí vive la base del conteo.

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
| `sin grabación ... tras 3m0s el índice de la cámara sigue llegando solo hasta HH:MM:SS` | la cámara dejó de grabar, o su índice está trabado | grabación de la cámara desde su web |
| Los clips de la G1 salen verdes o idénticos entre sí | se extrajo por RTSP, que en la G1 está roto | el log debe decir `por descarga`; si dice `por rtsp`, revisar el firmware |
| Los clips tardan 1 a 2 minutos en aparecer | la G1 indexa su grabación con atraso; el binario reintenta | normal; con `-debug` se ven los `se reintenta en 20s` |
| `warning deviation in data` justo después de un arranque | el gateway estuvo sin recibir eventos y la cámara reinició su contador | normal: se pierde a lo sumo ese paso y el siguiente ya cuenta |
| La plataforma no cambia nada | el flag está en el lanzador, el atributo no está dado de alta, o `nats-gwIot` no tiene sesión | banner (`[flag]`), log `config:` |
| `el horario de grabación ... NO quedó guardado` | la cámara responde OK pero ignora el horario (la G1 lo hace) | fijarlo a mano desde la web de la cámara, si hace falta |
| El conteo se detiene todas las noches | el gateway se reinicia a las 23:50 y el servicio no arranca solo | `ps \| grep omvz7/camera`; ver "Limitaciones" |
| Clips congelados, `max_gap_s` en segundos | H.264+ activo en la cámara | `-smartCodec off` |
| `almacenamiento en "unformatted"` | la SD de la cámara no sirve para grabar | formatear la cámara a mano; el binario no lo hace |
| `rechazó las credenciales` | `HIKVISION_CREDENTIALS` incorrecta | regenerar el valor y reiniciar el binario |
| `NO quedó guardado` | el valor pedido no está entre los que admite el modelo | `GET /ISAPI/Streaming/channels/101/capabilities` |

Un `401` de la cámara **apaga el diálogo con esa cámara hasta el próximo arranque**: la cámara
bloquea el usuario tras varios fallos, así que insistir cada ciclo con una credencial mala la
dejaría inaccesible en campo.

---

## Limitaciones conocidas

- **El servicio no vuelve solo después de un reinicio del gateway.** En el banco, el equipo se
  reinicia a las 23:50 y `/etc/init.d/backdoor` no tiene enlace en `rc*.d`: el conteo queda
  detenido hasta que alguien lo levanta. Es una decisión de despliegue pendiente, no del binario.
- **La puerta se deduce de la IP de origen.** Si los eventos llegan por NAT o reenvío de puertos,
  todas las cámaras llegan con la IP del router y se cuentan en la misma puerta.
- **El pre-roll es uno solo para todas las cámaras**, y tiene que cubrir a la más lenta (la G0
  manda el evento hasta 7 s después del cruce).
- **La G1 deja ~0.9 s sin grabar cada vez que cambia de archivo** (cada ~5 minutos). Un cruce que
  caiga justo ahí se ve con un salto. No hay forma de evitarlo desde el binario.
- **La G1 tarda hasta ~2 minutos en indexar lo grabado.** Mientras un clip espera, la cola de
  video espera con él: en un vehículo con dos cámaras, el clip de la otra puede demorarse lo
  mismo. El conteo no se afecta.
- **Bajar un clip de la G1 lee el archivo desde su comienzo** (archivos de hasta 5 minutos, ~74 MB
  sobre el cable a la cámara). No ocupa disco del gateway, pero sí red local y CPU.
- **El binario no borra clips.** Si el proceso que los sube no lo hace, la extracción se detiene
  al llegar a `-videoMinFree`.
- **La configuración de plataforma depende de `nats-gwIot`.** Sin él, o sin sesión con la
  plataforma, el binario usa los flags, los defaults y la última configuración guardada en disco.
- **Las credenciales están ofuscadas, no protegidas** (ver "Credenciales").

---

## Compilación

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -ldflags="-s -w" -o hikvision-<version>-g<hash>-armv7 ./client/main
```

Requiere Go 1.25. Las dependencias se bajan por versión publicada, **salvo una**:
`protoactor-go` se resuelve desde `../../asynkron/protoactor-go`, un clon local con un parche que
**evita un panic en ARMv7**. Por eso el repositorio tiene que estar en
`$GOPATH/src/github.com/dumacp/go-hikvision` con ese clon presente. Quitar ese `replace` compila
y pasa las pruebas en una PC, pero el binario se cae al arrancar en el vehículo.

`go test ./...` tiene que pasar completo; un `FAIL` es una regresión.

Conviene nombrar el artefacto con la versión y el hash del commit: la versión sola no identifica un
binario, porque varios commits pueden compartirla.
