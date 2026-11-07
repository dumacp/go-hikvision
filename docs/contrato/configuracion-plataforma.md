# Configuración desde la plataforma — `DEV_CAMERACOUNT_CONFIG`

Esta es la **tercera frontera** del binario, y la única que va de la plataforma hacia el
vehículo: el atributo del mapa de parámetros con el que se configura el conteo de pasajeros
por cámara sin entrar por SSH a editar el lanzador de cada equipo.

---

## El atributo

| | |
|---|---|
| **Clave** | `DEV_CAMERACOUNT_CONFIG` |
| **Tipo** | `string` que **contiene** JSON (doble codificación) |
| **Bucket** | `<orgId>_CFG-PARAMS`, llave = hostname del gateway (o el `-id` que se pase) |
| **Lo consume** | `go-hikvision` ≥ 1.0.41 |
| **Lo transporta** | `go-params` ≥ la versión que mapea la clave, vía `nats-gwIot` |

**La doble codificación no es un capricho y no se puede cambiar de un lado solo.** El parseo
de `go-params` recorre el mapa plano y hace `v.(string)`; si el atributo viajara como objeto
JSON anidado, el type assertion falla y la clave **se descarta con un `continue`, sin un solo
log**. Mismo tratamiento que `DEV_COUNTERPASS_CONFIG` y `DEV_TERMINAL_DRIVER_CONFIG`.

Y por la misma razón, **una clave que `go-params` no tenga en su `switch` tampoco llega**:
publicar el atributo no alcanza si el binario de parámetros no lo mapea.

## El JSON de adentro

```json
{
  "cameras": [
    {"ip": "192.168.186.93", "door": 0, "zeroOpenState": true,  "countWithCloseDoor": true},
    {"ip": "192.168.186.91", "door": 1, "zeroOpenState": true,  "countWithCloseDoor": false}
  ],
  "ntpServer": "ntp1.inm.gov.co",
  "videoCodec": "h265",
  "smartCodec": "off",
  "videoFrameRate": 12,
  "videoEnabled": true,
  "videoDir": "/SD/video265/",
  "videoBurstGap": 10,
  "videoMaxDuration": 60,
  "cameraPowerGPIO": 165,
  "cameraPowerOffLevel": 1
}
```

| campo | tipo | qué hace |
|---|---|---|
| `cameras[].ip` | string | dirección de la cámara, **sin puerto** |
| `cameras[].door` | int 0..15 | **obligatorio** si hay cámara: 0 es delantera, 1 trasera |
| `cameras[].zeroOpenState` | bool | `true` si el estado "abierta" de esa puerta llega como `0` |
| `cameras[].countWithCloseDoor` | bool | cuenta aunque la puerta esté cerrada |
| `ntpServer` | string | servidor NTP que se le configura a las cámaras; vacío no toca el reloj |
| `videoCodec` | string | `h264` o `h265` |
| `smartCodec` | string | `on` u `off` (H.264+) |
| `videoFrameRate` | int | cuadros por segundo del stream principal |
| `videoEnabled` | bool | **prende y apaga la extracción de clips** |
| `videoDir` | string | ruta absoluta donde se guardan los clips |
| `videoBurstGap` | int, **segundos**, 0..600 | un paso que llega hasta este tiempo después del anterior de la misma puerta va al mismo clip; `0` vuelve a un clip de ventana fija por paso |
| `videoMaxDuration` | int, **segundos**, 0..600 | tope de duración de un clip que crece por una ráfaga |
| `cameraPowerGPIO` | int | línea de sysfs que corta la energía de la cámara; `0` deshabilita |
| `cameraPowerOffLevel` | int | `0` o `1`: el nivel que significa APAGAR |

### `cameras` reemplaza la lista entera

No es un parche. Mandar **una sola** cámara deja la otra puerta **sin** cámara. Es lo que
hace falta para poder quitar una desde la plataforma, pero usarlo mal no da ningún error: se
ve como una serie de conteo que se congela.

### Un campo ausente NO cambia nada

Todos los campos escalares son opcionales y **ausente ≠ `false` ≠ `0`**. Omitir
`videoEnabled` conserva el valor que el equipo tenía; mandar `"videoEnabled": false` lo
apaga. Es la diferencia con el merge de `go-driverconsole` y `go-counterpass`, donde un
booleano omitido se lee como `false` y apaga el valor con el que se arrancó el equipo.

### Un campo nuevo no rompe los equipos viejos

El parseo ignora las claves que no conoce. Se puede agregar un campo para una versión futura
del binario sin tumbar la configuración de la flota que todavía no la tiene.

---

## Prioridad: **el flag de la línea de comandos gana**

Esto es **lo contrario** de lo que hacen `go-driverconsole` y `go-counterpass`, y hay que
tenerlo presente al cargar la configuración:

```
1. flag pasado explícitamente en /etc/init.d/backdoor   ← gana siempre
2. DEV_CAMERACOUNT_CONFIG                               ← solo rellena lo que el flag no fijó
3. default compilado
```

**Consecuencia operativa**: un equipo cuyo lanzador pase `-videoFrameRate` **no** va a
obedecer el `videoFrameRate` de la plataforma. Para delegarle un parámetro a la plataforma
hay que **quitar ese flag del lanzador**.

El binario lo deja escrito en el log de arranque y en cada aplicación, parámetro por
parámetro:

```
[ info ] configuración efectiva al arrancar:
  puerta 0             192.168.186.93     [plataforma/default]  zeroOpenState=true  [plataforma/default]  ...
  videoFrameRate       20                 [flag]
  videoEnabled         false              [plataforma/default]
```

---

## Cuándo se aplica

**En caliente.** No hay reinicio del proceso ni del conteo: `CountingActor` reconstruye solo
los actores que el cambio afecta y los acumulados quedan intactos.

| cambia | qué se rehace |
|---|---|
| `zeroOpenState`, `countWithCloseDoor` | nada; se aplica en el lugar |
| `cameras` | el clasificador de eventos, el keep-alive, el mantenimiento de cámara y el video |
| `ntpServer`, `videoCodec`, `smartCodec`, `videoFrameRate` | el mantenimiento de cámara |
| `videoEnabled`, `videoDir`, `videoBurstGap`, `videoMaxDuration` | la extracción de video |
| `cameraPowerGPIO`, `cameraPowerOffLevel` | la línea de GPIO, con una secuencia segura |

**Apagar `videoEnabled` descarta la cola de extracciones pendientes.** El conteo no se ve
afectado; algunos clips sí.

**Un cambio de GPIO deja primero la línea vieja en el nivel de "encendida"** y recién después
toma la nueva, para no abandonar una línea manejando el nivel de apagado.

Hay un retardo de ~10 s desde que arranca el binario hasta el primer descubrimiento, así que
el equipo **siempre** arranca con los flags, los defaults y el cache en disco, y la
plataforma llega después.

---

## Qué pasa si algo falla

| situación | comportamiento |
|---|---|
| sin red, o `nats-gwIot` caído | el binario cuenta igual, con flags, defaults y cache. Reintenta solo cada 60 s |
| atributo ausente o vacío | se conserva la configuración vigente, con WARN. **No** vuelve a los defaults |
| JSON inválido | se conserva la configuración vigente **entera**, con ERROR que nombra el campo |
| `door` repetido, IP con puerto, codec inexistente | se rechaza la configuración completa; nunca se aplica a medias |
| `cameraPowerGPIO` de plataforma que no se puede tomar | se deshabilita **solo** el apagado de la cámara y el conteo sigue |

Esa última fila es deliberada y vale explicarla: cuando el número de GPIO viene de un **flag**
y falla, el binario **se detiene** —quien lo lanzó escribió algo que el equipo no puede
hacer—. Cuando viene de la **plataforma**, detenerse dejaría a toda la flota sin arrancar y
sin forma de corregirlo desde el lanzador, así que se degrada en vez de caerse.

---

## El cache en disco

El último JSON válido se guarda en `/SD/boltdbs/cameracount-config.json` (configurable con
`-platformConfigCache`, vacío lo deshabilita). Se escribe como `.part` y se renombra, que es
atómico: un corte de energía a mitad de la escritura deja el archivo anterior intacto.

Existe porque **el cache de `go-params` no funciona**: escribe el mapa plano crudo y lo lee
como otro esquema, con lo que siempre recupera un struct vacío, y además nunca propaga lo
leído. Sin un cache propio, un arranque sin red deja el equipo con los defaults — y con los
flags quitados del lanzador, el default de `zeroOpenState` **descarta todos los cruces** en
un vehículo cableado al revés.

---

## Cómo viaja

```
Plataforma ── NATS JetStream KV, bucket "<orgId>_CFG-PARAMS", key = hostname
     │          TLS/WSS + OIDC Keycloak (realm DEVICES)
     ▼
nats-gwIot ── proceso aparte del gateway, es quien tiene las credenciales
     ▲ descubrimiento:  go-hikvision publica en  GWIOT/discovery  {"reply":"cameracountdiscovery/params"}
     │                  nats-gwIot responde en   cameracountdiscovery/params  {"host":...,"port":...}
     │ datos: protoactor remote contra ese host:port, con un WatchKeyValue del bucket
     ▼
go-hikvision
```

El binario **nunca** habla con la plataforma ni abre una conexión NATS. El tópico de reply es
propio de este binario: si dos procesos del gateway comparten uno, cada uno recibe las
respuestas del otro.

---

## Para deshabilitarlo entero

`-disablePlatformConfig`. El binario se comporta exactamente como antes de que esto
existiera: no abre el puerto de protoactor remote, no se suscribe a nada y no lee el cache.
