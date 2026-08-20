# Índice de endpoints ISAPI verificados

Extraído de los PDF oficiales en [docs/](../../../../docs/). Todos los cuerpos de petición y
respuesta son XML salvo donde se indique `?format=json`.

**Confirma contra el equipo real antes de usar**: el set de endpoints depende de modelo y
firmware. `GET /ISAPI/System/deviceInfo` da modelo y versión; cada rama tiene su
`.../capabilities`.

## Conteo de personas

Fuente: `ISAPI_peopleCounting.pdf` cap. 8. `<ID>` = canal de video (típicamente `1`).

| Método | Ruta | Petición → Respuesta | §    |
|---|---|---|---|
| GET | `/ISAPI/System/Video/capabilities` | — → `XML_VideoCap` (`isSupportCounting`) | 8.6 |
| GET | `/ISAPI/System/Video/inputs/channels/<ID>/counting` | — → `XML_Counting` | 8.9 |
| PUT | `/ISAPI/System/Video/inputs/channels/<ID>/counting` | `XML_Counting` → `XML_ResponseStatus` | 8.9 |
| GET | `/ISAPI/System/Video/inputs/channels/<ID>/counting/capabilities` | — → `XML_CountingCap` | 8.10 |
| GET | `/ISAPI/System/Video/inputs/channels/<ID>/counting/status` | — → `XML_CountingStatus` (`status`, `doorStatus`) | 8.16 |
| GET | `/ISAPI/System/Video/inputs/channels/<ID>/counting/parameterExport` | exporta parámetros | 8.11 |
| GET/PUT | `/ISAPI/System/Video/inputs/channels/<ID>/counting/posInfoOverlay` | `XML_PosInfoOverlay` | 8.12 |
| GET/PUT | `/ISAPI/System/Video/inputs/channels/<ID>/counting/reverseAlarm?format=json` | `JSON_reverseAlarm` | 8.13 |
| POST | `/ISAPI/System/Video/inputs/channels/<ID>/counting/search` | `XML_CountingStatisticsDescription` → `XML_CountingStatisticsResult` | 8.14 |
| GET | `/ISAPI/System/Video/inputs/channels/<ID>/counting/search/capabilities` | — → `XML_CountingSearchCap` | 8.15 |
| POST | `/ISAPI/System/Video/inputs/channels/counting/search` | búsqueda multicanal (NVR) | 8.7 |
| GET | `/ISAPI/System/Video/inputs/channels/counting/search/capabilities` | — → `XML_CountingSearchCap` | 8.8 |
| DELETE | `/ISAPI/ContentMgmt/FlashStorage/remove/channels/<ID>` | `XML_FlashStorageRemove` | 8.1 |

`XML_Counting` mínimo para habilitar:

```xml
<Counting version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
  <enabled>true</enabled>
</Counting>
```

Otros nodos de `XML_Counting` (§9.4): `MountingConfiguration` (`viewingAngle` `vertical|tilt`,
`mountHeight` cm, `horizontalDistance`, `focalLength`), `OverlayConfiguration` (OSD, `OSDType`
`enter|leave|entreLeave|peoplePassing`, `child`), `Demarcation` (regiones y línea de conteo),
`ChildFilter`, `MisinfoFilter`, `detectionMode`, `TrajectoryCountFilter`, `RegionsDirectionList`,
`EmailReport`, `maintenanceModeEnabled`.

`XML_CountingStatus` (§9.9):

```xml
<CountingStatus version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
  <status>counting</status>        <!-- counting | stopped | paused -->
  <time>22:00:00+08:00</time>
  <doorStatus>open</doorStatus>    <!-- open | close | N_A -->
</CountingStatus>
```

## Notificación de eventos

| Método | Ruta | Petición → Respuesta | § |
|---|---|---|---|
| GET | `/ISAPI/Event/notification/httpHosts/capabilities` | — → `XML_HttpHostNotificationCap` (`hostNumber`) | 8.20 |
| GET | `/ISAPI/Event/notification/httpHosts` | — → `XML_HttpHostNotificationList` | 8.18 |
| PUT | `/ISAPI/Event/notification/httpHosts` | `XML_HttpHostNotificationList` (reemplaza todos) | 8.18 |
| POST | `/ISAPI/Event/notification/httpHosts` | `XML_HttpHostNotification` (agrega uno) | 8.18 |
| DELETE | `/ISAPI/Event/notification/httpHosts` | borra todos | 8.18 |
| POST | `/ISAPI/Event/notification/httpHosts/<ID>/test` | `XML_HttpHostNotification` → `XML_HttpHostTestResult` | 8.19 |
| GET | `/ISAPI/Event/notification/alertStream` | stream `multipart/mixed` persistente | 8.17 |
| GET/PUT | `/ISAPI/Event/triggers/<ID>` | `XML_EventTrigger` | 8.3 |
| GET/PUT | `/ISAPI/Event/schedules/reverseEntrance/<ID>` | `XML_Schedule` | 8.2 |
| GET/PUT | `/ISAPI/Intelligent/channels/<ID>/Shield/<ID>` | `XML_Shield` (área blindada) | 8.4 |
| GET | `/ISAPI/Intelligent/channels/<ID>/Shield/capabilities` | — → `XML_ShieldCap` | 8.5 |

Query opcional `?security=1|2` en `httpHosts`: cifra los nodos sensibles (usuario/contraseña) en
AES128/AES256 CBC. Sin el parámetro van en claro.

`XML_HttpHostNotification` para apuntar la cámara a nuestro listener (§9.24):

```xml
<HttpHostNotification version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
  <id>1</id>
  <url>http://192.168.188.23:8088/</url>
  <protocolType>HTTP</protocolType>
  <parameterFormatType>XML</parameterFormatType>
  <addressingFormatType>ipaddress</addressingFormatType>
  <ipAddress>192.168.188.23</ipAddress>
  <portNo>8088</portNo>
  <httpAuthenticationMethod>none</httpAuthenticationMethod>
  <eventType>counting</eventType>
</HttpHostNotification>
```

`XML_EventTrigger` (§9.11) — **sin `notificationMethod` que incluya `HTTP` el evento no se
empuja**:

```xml
<EventTrigger version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
  <id>counting-1</id>
  <eventType>counting</eventType>
  <videoInputChannelID>1</videoInputChannelID>
  <intervalBetweenEvents>1</intervalBetweenEvents>
  <EventTriggerNotificationList>
    <EventTriggerNotification>
      <id>1</id>
      <notificationMethod>HTTP</notificationMethod>
      <notificationRecurrence>beginning</notificationRecurrence>
    </EventTriggerNotification>
  </EventTriggerNotificationList>
</EventTrigger>
```

- `notificationMethod` opciones: `email,IM,IO,syslog,HTTP,FTP,beep,ptz,record,monitorAlarm,center,LightAudioAlarm,focus,trace,cloud,SMS,whiteLight,audio`
- `notificationRecurrence`: `beginning`, `beginningandend`, `recurring` (+ `notificationInterval` ms).
  **`beginningandend` duplica el conteo de tamper en este binario** — ver "Discrepancias" en SKILL.md.
- `eventType` relevantes: `counting` (conteo), `tamperdetection` (tamper/`shelteralarm`),
  `reverseEntrance` (entrada en sentido inverso). Lista completa en §9.11.

## Sistema (ISAPI_general.pdf)

| Método | Ruta | Uso |
|---|---|---|
| GET | `/ISAPI/System/deviceInfo` | modelo, serie, firmware |
| GET | `/ISAPI/System/deviceInfo/capabilities` | capacidad del dispositivo |
| GET | `/ISAPI/System/status` | CPU y memoria |
| PUT | `/ISAPI/System/reboot` | reinicio |
| PUT | `/ISAPI/System/shutdown?format=json` | apagado |
| PUT | `/ISAPI/System/factoryReset?mode=` | restaurar fábrica |
| GET/PUT | `/ISAPI/System/time` | hora del equipo |
| GET/PUT | `/ISAPI/System/time/localTime`, `/timeZone`, `/ntpServers` | hora detallada y NTP |
| GET | `/ISAPI/Streaming/channels/<ID>/picture` | captura JPEG |

## Hora y NTP

Fuente: `ISAPI_general.pdf` §16.11.229–16.11.238. La columna "equipo" indica lo comprobado en el
DS-2XM6825G0 (V5.5.850).

| Método | Ruta | Mensaje | Equipo |
|---|---|---|---|
| GET / PUT | `/ISAPI/System/time` | `XML_Time` | ✅ GET |
| GET | `/ISAPI/System/time/capabilities` | `XML_Cap_Time` | ✅ `timeMode opt="NTP,manual"` |
| GET / PUT | `/ISAPI/System/time/localTime` | fecha-hora ISO 8601 | no probado |
| GET / PUT | `/ISAPI/System/time/timeZone` | cadena POSIX | ✅ `CST+5:00:00` |
| GET / PUT / POST / DELETE | `/ISAPI/System/time/ntpServers` | `XML_NTPServerList` (PUT reemplaza todo) / `XML_NTPServer` (POST agrega uno) | ✅ GET |
| GET / PUT / DELETE | `/ISAPI/System/time/ntpServers/<ID>` | `XML_NTPServer` | no probado |
| POST | `/ISAPI/System/time/ntpServers/test` | `XML_NTPTestDescription` → `XML_NTPTestResult` | ✅ `errorCode 0`, `ok` |
| GET | `/ISAPI/System/time/ntpServers/capabilities` | — | ❌ `statusCode 3` Device Error |
| GET / PUT | `/ISAPI/System/time/timeType?format=json` | `JSON_TimeType` (`local`/`UTC`) | ❌ `statusCode 4` Invalid Operation |

**No existe un endpoint de "sincronizar ahora".** `ntpServers/test` solo comprueba que el servidor
esté disponible; no dispara sincronización. Las únicas primitivas son re-aplicar la configuración
(`PUT` de `time` o de `ntpServers`) o escribir el reloj a mano con `timeMode=manual` + `localTime`
— lo que **abandona NTP** hasta que se restaure.

`XML_Time`:

```xml
<Time version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
  <timeMode>NTP</timeMode>              <!-- manual,NTP,local,satellite,timecorrect (este equipo: NTP,manual) -->
  <localTime>2026-08-12T13:07:06-05:00</localTime>  <!-- requerido con manual/local -->
  <timeZone>CST+5:00:00</timeZone>      <!-- requerido con manual/local/NTP -->
  <satelliteInterval>1440</satelliteInterval>       <!-- minutos, solo con timeMode=satellite -->
</Time>
```

`XML_NTPServer`:

```xml
<NTPServer version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
  <id>1</id>
  <addressingFormatType>hostname</addressingFormatType>  <!-- ipaddress | hostname -->
  <hostName>ntp2.inm.gov.co</hostName>
  <portNo>123</portNo>
  <synchronizeInterval>60</synchronizeInterval>           <!-- MINUTOS -->
</NTPServer>
```

Dos trampas verificadas:

- **`timeZone` es POSIX y tiene el signo invertido**: `CST+5:00:00` significa **UTC−5**, correcto
  para Colombia. Escribir `CST-5:00:00` deja la cámara 10 horas corrida.
- **`synchronizeInterval` está en minutos.** El equipo verificado tenía **1500 = 25 horas**: con NTP
  bien configurado y el servidor respondiendo `ok`, solo re-sincroniza una vez al día. Es la
  explicación más probable de "tiene NTP pero no sincroniza bien".

## Video: RTSP, grabación y almacenamiento

Medido en el DS-2XM6825G0 (V5.5.850) el 2026-08-18.

### RTSP

| Uso | URL | Verificado |
|---|---|---|
| En vivo | `rtsp://<ip>:554/ISAPI/Streaming/channels/101` | ✅ `DESCRIBE` → 200 OK |
| En vivo, forma corta | `rtsp://<ip>:554/Streaming/channels/101` | ✅ 200 OK |
| Reproducción por hora | `rtsp://<ip>:554/ISAPI/Streaming/tracks/101?starttime=&endtime=` | ❌ 500 (sin grabaciones) |

`starttime`/`endtime` en ISO 8601 (§16.10.17); `endtime` es opcional y sin él el stream sigue
hasta cerrar la sesión. Digest aplica igual que en HTTP, con `uri` = la URL RTSP completa. Para
`SETUP` hay que usar la URL del track (`a=control` del SDP, `.../channels/101/trackID=1`): sobre la
URL agregada devuelve **415 Unsupported Media Type**.

**La reproducción por `starttime` exige grabaciones en la cámara.** Con el almacenamiento sin
formatear devuelve `500 Internal Server Error`, y eso hace fallar el `ffmpeg -i
rtsp://...?starttime=...` aunque la sintaxis sea correcta.

### Parámetros reales del stream

Lo declarado y lo medido no coinciden, y las dos trampas importan:

| | Valor |
|---|---|
| `maxFrameRate` declarado | `2000` → **son centi-fps: 20 fps**, no 2000 |
| `Description` del track de grabación | `framerate=2.080000 fps` → **metadato erróneo** |
| framerate medido (400 frames en 20 s) | **19.97 fps** |
| `constantBitRate` declarado | `4096` kbps → es el **techo VBR**, no el consumo |
| bitrate medido | **205 kbps** |
| volumen | 92 MB/hora |
| clip de 10 s | 0.26 MB |
| retención en una SD de 7695 MB | ~83 horas |

Dimensionar con el `4096` declarado sobreestima el disco por 20x. Medí el bitrate real contando
bytes de RTP antes de prometer retención o tamaños.

### Almacenamiento y grabación

| Método | Ruta | Notas |
|---|---|---|
| GET | `/ISAPI/ContentMgmt/Storage` | lista `hddList`; `status` `unformatted` significa que la cámara no puede usarla |
| GET | `/ISAPI/ContentMgmt/Storage/hdd/<ID>` | estado de un medio |
| PUT | `/ISAPI/ContentMgmt/Storage/hdd/<ID>/format?formatType=EXT4` | **destructivo**; `FAT32` es el default y limita el archivo a 4 GB |
| GET | `/ISAPI/ContentMgmt/Storage/hdd/<ID>/formatStatus` | progreso del formateo |
| GET | `/ISAPI/ContentMgmt/record/tracks` | tracks y su `DefaultRecordingMode` (`CMR` = continuo) |
| GET/PUT | `/ISAPI/ContentMgmt/record/tracks/<ID>` | habilitar y programar la grabación |
| POST | `/ISAPI/ContentMgmt/search` | buscar grabaciones (`CMSearchDescription`) |
| POST | `/ISAPI/ContentMgmt/download` | descargar un tramo por HTTP, alternativa a RTSP |

En el equipo verificado el track 101 ya tiene `Enable=true` y `CMR`, así que **basta formatear el
medio para que empiece a grabar**.

### Extracción de un tramo: lo que funciona y lo que engaña

Verificado el 2026-08-18 con la SD formateada (EXT4) y grabación activa, ffmpeg 6.1.1.

**Funciona** — RTSP playback con ffmpeg, con precisión al segundo:

```bash
ffmpeg -rtsp_transport tcp \
  -i "rtsp://USER:PASS@IP:554/Streaming/tracks/101/?starttime=20260818T155900Z" \
  -t 10 -c copy clip.mp4
```

Resultado medido: MP4 válido, H.264 1440x900, 201 frames en 10.009 s (**20.08 fps**), 243 KB. El
OSD del video confirmó `08-18-2026 Tue 10:59:00`, exactamente el `starttime` pedido en UTC pasado
a hora local. Los dos formatos de hora sirven: el compacto `20260818T155900Z` que devuelve el
propio `search`, y el `2026-08-18T15:59:00` del ejemplo original.

Bonus para auditoría: **el OSD lleva quemados la fecha, la hora y los contadores**
(`Enter:16 / Leave:15`), así que un clip se puede cotejar visualmente contra el evento.

**No funciona** — `POST /ISAPI/ContentMgmt/download` devuelve `statusCode 4` /
`methodNotAllowed` en este modelo, igual que `download/capabilities` y `search/profile`. La vía
"solo HTTP, sin RTSP ni ffmpeg" **no existe acá**, aunque el PDF la documente.

**Tres trampas operativas, todas medidas:**

1. **Pedir una hora sin grabación NO da error: da el video equivocado.** Con `starttime` del día
   anterior (sin grabación) la cámara devolvió `exit=0`, 244 KB y un MP4 válido... del **inicio de
   la única grabación disponible**. Un clip así queda etiquetado con un evento y muestra otro
   momento. **Hay que verificar con `POST /ISAPI/ContentMgmt/search` que exista cobertura antes de
   extraer**, y usar el `startTime`/`endTime` que devuelve para acotar la ventana.
2. **`endtime` en la URL sin `-t` cuelga ffmpeg indefinidamente** (probado: 2 minutos sin salir).
   Siempre acotar con `-t` y además un timeout externo al proceso.
3. **La entrega es del orden del tiempo real, y depende del volumen del tramo**: 10 s de video a
   20 fps tardaron 10.3 s, pero 8 s de un tramo a ~6 fps tardaron 6.2 s. Hay que encolar la
   extracción y no contar con recuperar una ráfaga al instante, pero tampoco es un 1:1 estricto.
   **El framerate del tramo grabado varía**: se midieron 20 fps (201 frames en 10 s) y ~6 fps
   (49 frames en 8 s) en la misma cámara, así que no asumas una tasa fija para dimensionar.
4. **No se puede extraer el video de un evento recién ocurrido.** Un recorte con post-roll termina
   en el futuro respecto del instante del evento, así que esa parte todavía no está grabada y la
   verificación de cobertura falla. Hay que esperar a que la ventana completa quede grabada más un
   margen. El índice de la cámara **no** va atrasado: encuentra cobertura hasta 5 s atrás.
5. **El clip real puede empezar hasta un GOP antes del `starttime` pedido** (medido: 2 s a 20 fps),
   porque la cámara posiciona la reproducción en el keyframe anterior. Da más pre-roll que el
   configurado, nunca menos, pero el instante pedido no es el primer frame exacto.

`isapi.SearchRecordings` y `isapi.HasCoverage` implementan la guarda del punto 1, y el paquete
`video` la extracción en Go puro, sin ffmpeg.

### El framerate de la grabación no es constante: los clips salen congelados

Medido el 2026-08-18 en dos clips de la misma cámara, extraídos con el mismo código:

| Clip | Escena | Frames | Distribución |
|---|---|---|---|
| 10:59 | con actividad | 201 en 10 s | uniforme, delta máx. 0.083 s |
| 14:56 | sala quieta y luego una persona cruzando | 49 en 8 s | keyframe en `0.000`, **keyframe en `5.667`**, y 47 frames a 24 fps entre `5.708` y `8.000` |

En el segundo hay un **hueco de 5.7 segundos sin un solo frame**: la cámara grabó un keyframe cada
~6 s con la escena estática y disparó a 24 fps al detectar movimiento. El reproductor sostiene el
último frame, así que el clip **parece congelado** y el reloj del OSD se queda clavado, aunque el
archivo es correcto: reproduce fielmente lo que se grabó.

Consecuencias prácticas:

- **El pre-roll pierde su valor**: los segundos previos al evento son un keyframe único, no se ve a
  la persona acercándose.
- **El cruce queda comprimido** en los últimos segundos del recorte.
- No es un defecto del extractor. Antes de culpar al código, mirá la distribución de `pts_time`:
  `ffprobe -select_streams v:0 -show_entries frame=pts_time,key_frame -of csv=p=0 clip.mp4`

**Confirmado y corregido: era H.264+ / SmartCodec.** El nodo vive dentro de `<Video>` en
`GET /ISAPI/Streaming/channels/101`:

```xml
<SmartCodec><enabled>true</enabled></SmartCodec>
```

No hay sub-recurso `/video/SmartCodec` (devuelve `statusCode 4`), así que se cambia con un **PUT de
la configuración completa del canal**, guardando antes el XML original. El PUT responde
`statusCode 7` / `Reboot Required`: hasta que se ejecute `PUT /ISAPI/System/reboot` el cambio no
toma efecto. **El reinicio corta el tramo grabado en dos**, y pedir un instante del hueco entre
ambos devuelve `500` en el DESCRIBE.

Resultado medido con `SmartCodec: false`, mismo código de extracción:

| | frames en 8-10 s | delta máximo | bitrate |
|---|---|---|---|
| SmartCodec **on**, escena quieta | 49 | **5.667 s** | 195 kbps |
| SmartCodec **off**, escena quieta | 161 en 8 s | 0.083 s | — |
| SmartCodec **off**, con cruce | 201 en 10 s | 0.083 s | **507 kbps** |

El precio es **x2.6 en bitrate**: 228 MB/hora, y la retención de una SD de 7695 MB baja de ~10 días
a **4.2 días** con un horario de 8 h diarias (1.4 días si fuera 24/7). Sigue siendo cómodo para
extraer poco después del evento, pero hay que tenerlo en cuenta al dimensionar la tarjeta.

### Qué escritura exige reiniciar y qué no

Medido en el DS-2XM6825G0 (V5.5.850). Importa porque decide si un cambio se puede aplicar en
servicio o hay que esperar la franja de mantenimiento:

| Endpoint | Respuesta al PUT | ¿Reinicio? |
|---|---|---|
| `/ISAPI/System/time` | `statusCode 1` | no, en caliente |
| `/ISAPI/System/time/ntpServers` | `statusCode 1` | no, en caliente |
| `/ISAPI/ContentMgmt/record/tracks/101` | `statusCode 1`, activo al leerlo de vuelta | no, en caliente |
| `/ISAPI/Streaming/channels/101` | **`statusCode 7`** `Reboot Required` | **sí**, inerte hasta reiniciar |

El binario lo refleja: el perfil del encoder es lo último que revisa cada ciclo, y el PUT y el
reinicio van en el **mismo** ciclo. No pueden separarse: un cambio que responde `7` queda guardado
pero inerte y la cámara reporta el valor guardado, así que si el reinicio quedara para más tarde y
el binario arrancara de nuevo en el medio, el ciclo siguiente no vería diferencia y nadie
reiniciaría nunca.

El único freno es un mínimo de tiempo encendido (`encoderMinUptime`, 30 min): el tope de "un
reinicio por cámara" vive en memoria, y un binario en bucle de supervisión nunca llega a los 30
minutos. Hubo antes una ventana horaria y se quitó — el gateway se apaga de noche con el vehículo,
así que una franja de madrugada nunca se alcanzaba.

Dos trampas del `<Video>` de este endpoint:

- **`maxFrameRate` va en centi-fps**: `2000` son 20 fps. Escribir `20` deja la cámara a 0.2 fps.
- **`videoCodecType` no se toca desde el binario.** `video/extract.go` solo maneja H.264; poner
  H.265 rompe la extracción en silencio. Se reporta en el evento `CAMERATIME` y se deja WARN.

### Eventos individuales: el API no los tiene

`POST .../counting/search` devuelve agregados, con granularidad mínima de un cuarto de hora
(`MinTimeInterval` admite `quarter, half, hour, day, month, week`). Verificado: responde cubos
`00:00:00`–`00:14:59` con `enterCount`/`exitCount`.

Para recortar el video de un cruce hace falta el segundo exacto, y eso **solo existe en el push de
eventos** que el binario ya recibe (un evento `realTime` por cruce). El binario no persiste esas
marcas de tiempo: la boltdb guarda solo acumulados.

## Códigos de estado

`XML_ResponseStatus` (§9.16), namespace `http://www.std-cgi.org/ver20/XMLSchema`:

| `statusCode` | `statusString` |
|---|---|
| 0, 1 | OK |
| 2 | Device Busy |
| 3 | Device Error |
| 4 | Invalid Operation |
| 5 | Invalid XML Format |
| 6 | Invalid XML Content |
| 7 | Reboot Required |
| 9 | Additional Error |

`subStatusCode` describe la causa en detalle; `errorCode` es el hexadecimal convertido a decimal.
El apéndice A de `ISAPI_peopleCounting.pdf` lista los códigos específicos de conteo
(p. ej. `0x60000085` `DetectionLineOutofDetectionRegion`, `0x60000086` `DetectionRegionError`).

**Un HTTP 200 puede traer `statusCode` de error**: valida siempre el cuerpo.

## Cómo consultar los PDF

```bash
pdftotext -layout "docs/ISAPI_peopleCounting.pdf" /tmp/pc.txt
grep -n "9.10 XML_EventNotificationAlert" /tmp/pc.txt   # ubicar la sección
sed -n '1759,1830p' /tmp/pc.txt                          # leerla

# listar todos los endpoints de un documento
grep -ohE "/ISAPI/[A-Za-z0-9/<>_.?&=-]+" /tmp/pc.txt | sort -u
```
