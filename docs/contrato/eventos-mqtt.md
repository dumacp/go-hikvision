# Eventos MQTT

Broker **local** del gateway, `tcp://127.0.0.1:1883`, sin credenciales. Lo que sale de acá
lo recoge el agente de telemetría del equipo y lo reenvía a la plataforma.

---

## El sobre

Todos los mensajes tienen la misma forma:

```json
{"timestamp": 1790188800.123, "type": "<TIPO>", "value": { ... }}
```

- **`timestamp`** son segundos Unix con fracción, del **reloj del gateway**, tomados al
  publicar. No es la hora del cruce del pasajero — esa viaja adentro, en los mensajes que
  la traen.
- **`type`** identifica el mensaje.
- **`value`** es el contenido, distinto para cada tipo.

> **El agente de telemetría reempaqueta.** En la plataforma el mismo mensaje puede verse
> como `{"t":…,"tp":"COUNTERSDOOR","vl":{…}}` dentro de un sobre más grande con `sDv` y
> `state`. Esa transformación **no la hace este binario** y no está documentada acá; lo que
> sigue es lo que sale del broker local.

---

## Mapa de tópicos

| Tópico | Mensajes | ¿Nuevo? |
|---|---|---|
| `EVENTS/backcounter` | `COUNTERSDOOR`, `TAMPERING`, `CounterDisconnected` | no |
| `EVENTS/counterevents` | `CAMERATIME`, `COUNTERSDOORVIDEO`, `CAMERAPOWER` | **los tres mensajes, sí** |
| `COUNTERSMAPDOOR` | acumulados por puerta | no |
| `camera/doors` | `DOORS`, pedido de estado de puertas | no |

**Los mensajes nuevos van a `EVENTS/counterevents`, no a `EVENTS/backcounter`.** Un
consumidor del tópico histórico no los ve, y por eso agregarlos no pudo romper nada que ya
funcionara.

---

## `COUNTERSDOOR` — un pasajero contado

Tópico `EVENTS/backcounter`. **Es el único mensaje preexistente al que se le agregó un
campo.**

```json
{"timestamp":1790188800.123,"type":"COUNTERSDOOR","value":{
  "coord":"$GPRMC,151752.0,A,0609.900974,N,07536.109714,W,0.0,1.8,230926,4.7,W,A*07",
  "id":0,
  "state":1,
  "counters":[1,0],
  "type":"CAMERA",
  "event_id":"ce9f5d9d"}}
```

| Campo | |
|---|---|
| `coord` | trama `$GPRMC` cruda, o `""` si el GPS no respondió en 180 ms |
| `id` | puerta: `0` delantera, `1` trasera |
| `state` | estado de la puerta visto por el binario; `0` si nunca llegó un `MsgDoor` |
| `counters` | `[entradas, salidas]` — **incrementos de este evento**, no acumulados |
| `type` | siempre `CAMERA` |
| **`event_id`** | **NUEVO en 1.0.32.** Ocho hexadecimales. La llave para unir el paso con su video |

### Sobre `event_id`

- **Lleva `omitempty`**: si el evento no trae uid, la clave **no aparece**. Pasa con los
  eventos replicados de una base anterior a 1.0.32.
- **`-withoutEventID` la omite siempre**, devolviendo el mensaje al formato previo. Es una
  compuerta temporal para plataformas que todavía no procesan el campo. Mientras esté
  activa **el cruce conteo↔video es imposible desde este lado**.
- `counters` es `[1,0]` para una entrada y `[0,1]` para una salida. Un evento puede traer
  más de un pasajero; entonces el número es mayor que 1.

---

## `TAMPERING` — manipulación de la cámara

Tópico `EVENTS/backcounter`. **Sin cambios.** No lleva `event_id`.

```json
{"timestamp":1790188800.123,"type":"TAMPERING","value":{
  "coord":"$GPRMC,...","id":1,"state":0,"counters":[0,1],"type":"CAMERA"}}
```

---

## `CounterDisconnected` — la cámara dejó de responder

Tópico `EVENTS/backcounter`. **Sin cambios.**

```json
{"timestamp":1790188800.123,"type":"CounterDisconnected","value":{
  "coord":"$GPRMC,...","id":0,"type":"CAMARA"}}
```

> `type` dice **`CAMARA`**, en español y sin tilde, no `CAMERA` como los otros. Es así
> desde siempre y no se corrigió para no romper a quien ya lo consume. El `id` va fijo en
> `0` aunque el keep-alive sea de otra puerta.

---

## `COUNTERSDOORVIDEO` — el clip de un paso ya está en disco  🆕

Tópico `EVENTS/counterevents`. **Mensaje nuevo en 1.0.32.**

```json
{"timestamp":1790188800.123,"type":"COUNTERSDOORVIDEO","value":{
  "id":0,
  "type":"CAMERA",
  "event_id":"ce9f5d9d",
  "gateway_hostname":"FLO-W7-0036",
  "metadata":"20260908T160000_FLO-W7-0036_p0_in_ce9f5d9d.json",
  "clip":"20260908T160000_FLO-W7-0036_p0_ce9f5d9d.mp4",
  "camera_serial":"DS-2XM6825G0/C-IVS20221126AAWRK97545100"}}
```

| Campo | |
|---|---|
| `id` | puerta |
| `type` | siempre `CAMERA` |
| `event_id` | **el mismo** que trae el `COUNTERSDOOR` de ese paso |
| `gateway_hostname` | equipo donde corrió el binario |
| `metadata` | nombre del archivo `.json`, **sin ruta** |
| `clip` | nombre del archivo `.mp4`, **sin ruta** |
| `camera_serial` | identidad estable de la cámara; se omite si no se pudo consultar |

### Por qué existe este mensaje y no un campo en `COUNTERSDOOR`

Al contar el paso todavía **no se sabe** el nombre final del archivo ni si la extracción va
a funcionar: puede no haber grabación de ese instante, faltar espacio en disco o caerse la
cámara. Anunciarlo ahí dejaría a la plataforma esperando un archivo que quizá nunca exista.

El `COUNTERSDOOR` solo promete el id; **este mensaje confirma un hecho**. Si no llega, ese
paso no tiene video, sin ambigüedad.

Va **uno por evento** aunque varios compartan el mismo clip, para que el cruce sea 1 a 1 por
`event_id` y nadie tenga que desarmar listas. Ver [correlacion.md](correlacion.md).

---

## `CAMERATIME` — salud de la cámara y del gateway  🆕

Tópico `EVENTS/counterevents`. **Mensaje nuevo en 1.0.32.**

```json
{"timestamp":1790188800.123,"type":"CAMERATIME","value":{
  "id":1,
  "type":"CAMERA",
  "status":"storage",
  "camera":"192.168.186.91",
  "camera_serial":"DS-2XM6825G0/C-IVS20221126AAWRK97545100",
  "drift_s":-0.7,
  "time_mode":"NTP",
  "ntp_server":"ntp2.inm.gov.co",
  "ntp_interval_min":60,
  "storage":"unformatted",
  "video_dir":"/SD/video",
  "video_dir_free_mb":6842,
  "video_codec":"H.265",
  "video_fps":12,
  "smart_codec":false}}
```

### ⚠️ No es un latido

**Se publica SOLO en cambios de estado, nunca por ciclo.** Con una flota sana este mensaje
**no se envía nunca**. No sirve para saber que la cámara vive.

El ciclo de revisión corre cada `-cameraCheckInterval` (30 min por defecto), el primero a
los 90 s de arrancar, pero publica únicamente cuando algo cambia. Una condición que
persiste **avisa una vez y no vuelve a hablar** hasta que se corrija.

### Valores de `status`

| `status` | Qué pasó |
|---|---|
| `fixed` | se corrigió algo de la configuración; el detalle va en `fixed` |
| `drift` | el reloj derivó más de `-timeDriftMax` durante tres ciclos seguidos |
| `ok` | el reloj volvió a hora después de haber alarmado |
| `storage` | el medio de grabación de la **cámara** dejó de servir |
| `storage_ok` | se recuperó |
| `videodir` | el disco **del gateway** cruzó el piso de `-videoMinFree` |
| `videodir_ok` | se recuperó |
| `encoder_rejected` | se pidió un valor de codificación que la cámara no guardó |
| `reboot` | se está reiniciando la cámara para aplicar el perfil |

### Campos

| Campo | |
|---|---|
| `id` | puerta de la cámara |
| `status` | ver tabla de arriba |
| `camera` | IP, cambia con el direccionamiento |
| `camera_serial` | identidad estable; **preferila sobre la IP** |
| `drift_s` | deriva del reloj en segundos; positivo = cámara adelantada |
| `time_mode`, `ntp_server`, `ntp_interval_min` | lo observado en la cámara |
| `ntp_reachable` | **solo aparece cuando hubo deriva.** Su ausencia no significa que el NTP esté mal |
| `fixed` | lista de textos con lo que se corrigió |
| `storage` | estado del medio **de la cámara** |
| `storage_free_mb` | MB libres de la SD **de la cámara** |
| `video_dir` | directorio de clips **en el gateway** |
| `video_dir_free_mb` | MB libres del disco **del gateway** |
| `video_codec`, `video_fps`, `smart_codec` | perfil **observado**, antes de corregir |

### Tres cosas que se malinterpretan

**`storage_free_mb` y `video_dir_free_mb` son discos distintos.** El primero es la SD **de
la cámara**, de donde se lee la grabación; el segundo es `/SD` **del gateway**, donde se
escribe el clip. Dos fallas diferentes: no hay qué extraer, contra no hay dónde ponerlo.

**`storage` es texto libre, no un enum cerrado.** Trae lo que diga la cámara — `ok`,
`unformatted`, `error`, `idle` — más un valor propio del binario: **`nooverwrite`**, cuando
la sobrescritura cíclica está apagada y la cámara va a dejar de grabar al llenarse.
Trátenlo como cadena.

**No hay alarma por espacio libre bajo en la cámara, y es deliberado.** Con la
sobrescritura encendida —el default— la cámara borra lo más viejo y sigue grabando, así que
el espacio libre cerca de cero es lo **normal** en una cámara sana. Lo que se vigila es la
configuración, no el número.

**`video_dir_free_mb` viaja en todos los `CAMERATIME`**, no solo cuando falla. Su valor
está en la tendencia: cuando el piso se cruza ya es tarde.

**Los campos con `omitempty` desaparecen cuando valen cero o vacío.** `storage_free_mb` en
`0` no aparece. Su ausencia no significa "no se midió".

---

## `COUNTERSMAPDOOR` — acumulados por puerta

Tópico `COUNTERSMAPDOOR`. **Sin cambios.** Son los contadores acumulados que persisten entre
reinicios, no los incrementos.

---

## `DOORS` — pedido de estado de puertas

Tópico `DOORS`, con respuesta en `camera/doors`. **Sin cambios.** Es hacia el sistema de
puertas, no hacia la plataforma.


---

## `CAMERAPOWER` — la cámara se apagó a propósito  🆕

Tópico `EVENTS/counterevents`. Solo lo emiten los equipos configurados con
`-cameraPowerGPIO`, que le cortan la energía a la cámara cuando el vehículo se apaga.

```json
{"timestamp":1793335980.405,"type":"CAMERAPOWER","value":{
  "type":"CAMERA",
  "status":"poweroff",
  "gpio":164,
  "level":1,
  "source":"ignition"
}}
```

| `status` | qué pasó |
|---|---|
| `poweroff` | se cortó la energía: el vehículo se apagó |
| `poweron` | volvió la energía y la cámara ya tuvo tiempo de arrancar |
| `power_error` | la escritura a la GPIO falló; la línea **no** está donde dice la configuración |

**Este es el mensaje que distingue "apagada a propósito" de "averiada".** Sin él, un equipo
con la cámara sin energía durante la noche se ve exactamente igual que uno con la cámara
rota: los dos dejan de contar y los dos se quedan callados. Si van a consumir un solo
mensaje de este tópico, que sea este.

Va **una publicación por transición**, nunca por ciclo, con el mismo criterio que
`storage`/`storage_ok` del `CAMERATIME`.

**Mientras el último `status` sea `poweroff`, la ausencia de conteo es la esperada**, y
tampoco van a llegar `CAMERATIME` ni `COUNTERSDOORVIDEO` de esa cámara. El binario deja de
consultarla a propósito, así que **tampoco publica `CounterDisconnected`**: ese evento sigue
significando lo que significaba, una cámara que debería responder y no responde.

`gpio` y `level` son la línea y el nivel que significa apagado. Van en el mensaje para poder
auditar cómo quedó configurado cada equipo sin entrar por SSH.
