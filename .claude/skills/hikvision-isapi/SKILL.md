---
name: hikvision-isapi
description: Cómo hablarle a la cámara Hikvision desde este binario — ISAPI/REST con autenticación Digest, los dos modos de recepción de eventos (arming vs listening), cómo apuntar la cámara al listener, endpoints verificados contra la documentación oficial en docs/, y las discrepancias reales entre el parser del repo y la especificación. Úsala al agregar comandos hacia la cámara, al depurar por qué no llegan eventos, o al tocar el parseo de XML.
---

# Hikvision ISAPI desde go-hikvision

Hoy el binario es casi todo **entrada**: la cámara hace `POST` de XML al servidor HTTP en
`-socket` ([peoplecounting/listenner.go](../../../peoplecounting/listenner.go)) y el único
tráfico **saliente** es un `http.Get("http://192.168.188.21")` como keep-alive
([client/ping-actor.go](../../../client/ping-actor.go)). Los comandos hacia la cámara son
territorio nuevo.

## Fuentes

Documentación oficial en [docs/](../../../docs/), extraíble con `pdftotext -layout`:

| PDF | Contenido | Cuándo |
|---|---|---|
| `ISAPI_peopleCounting.pdf` (89 p.) | **el documento clave**: conteo de personas, httpHosts, evento `PeopleCounting`, triggers | casi siempre |
| `ISAPI_general.pdf` (946 p.) | autenticación, modelo de URI, `ResponseStatus`, catálogo de `eventType`, System/Network/Streaming | endpoints de sistema, errores |
| `ISAPI_thermal.pdf` (275 p.) | cámaras térmicas | no aplica a este proyecto |
| `HikVision Player SDK for linux v4.3.pdf` | Player SDK C/SDL (`Hik_PlayM4_*`), reproducción de video | **no es ISAPI**, no aplica |

Herramientas incluidas en esta skill:

- `scripts/check-camera.sh` — descubrimiento de **solo lectura** contra un equipo
  (`CAM_IP=… CAM_USER=… CAM_PASS=… ./check-camera.sh`). Aborta al primer 401 en vez de reintentar,
  porque la cámara **bloquea el usuario** tras N fallos.
- `scripts/capture-events.go` — servidor que registra crudo lo que la cámara empuja (headers,
  diagnóstico del filtro del handler, cuerpo). `go run capture-events.go -socket :8088`.

Las versiones importan: el doc de people counting es **ISAPI v2.0** (namespace
`http://www.isapi.org/ver20/XMLSchema`), mientras las cámaras desplegadas envían
**v1.0** con namespace `urn:psialliance-org` (ver el fixture del repo). Confirma siempre contra
`GET /ISAPI/System/deviceInfo` y `.../capabilities` del equipo real: el set de endpoints depende
de modelo y firmware.

## Autenticación

`ISAPI_general.pdf` §3.1: Digest (RFC 2617) o Basic; sin credenciales el equipo responde **401**.
Digest MD5 con `qop=auth`:

```
A1 = <user>:<realm>:<password>
A2 = <request-method>:<uri>
Digest = MD5( MD5(A1) : <nonce> : <nc> : <cnonce> : <qop> : MD5(A2) )
```

Tres cosas operativas:

- **`net/http` de la stdlib no implementa Digest.** Hay que hacer el flujo 401→reintento a mano
  o añadir dependencia; hoy el repo no tiene ninguna, así que es una decisión a acordar.
- **El usuario se bloquea.** La spec dice que ante fallo se devuelve
  `ResponseStatus_AuthenticationFailed` con los intentos restantes, y que al llegar a 0 la cuenta
  queda bloqueada. **En el firmware verificado eso no llega**: el 401 trae una página HTML
  (`Access Error: 401 -- Unauthorized`) sin ningún contador. O sea que no se puede ser prudente en
  función de los intentos que quedan — razón de más para tratar el 401 como **terminal** y no
  reintentar. Así lo hace `isapi.ErrUnauthorized`.
- Credenciales por flag o variable de entorno, nunca hardcodeadas (`pingIP` hardcodeada es deuda
  conocida, no un patrón a imitar).

Prueba manual: `curl --digest -u admin:CLAVE http://IP/ISAPI/System/deviceInfo`

## Los dos modos de recepción (§6 de peopleCounting)

**Listening mode** — el que usa este binario. La cámara empuja el evento por `POST` a un HTTP
host configurado en ella. Cadena completa:

1. `GET /ISAPI/Event/notification/httpHosts/capabilities` → cuántos hosts admite (`hostNumber`).
2. `GET /ISAPI/Event/notification/httpHosts` → estado actual (`XML_HttpHostNotificationList`).
3. `PUT /ISAPI/Event/notification/httpHosts` (reemplaza todos) o
   `POST /ISAPI/Event/notification/httpHosts` (agrega uno), con `XML_HttpHostNotification`:
   `url` absoluta al `-socket` del binario, `protocolType` `HTTP`, `parameterFormatType` `XML`,
   `addressingFormatType` `ipaddress`, `httpAuthenticationMethod` `none`.
4. `POST /ISAPI/Event/notification/httpHosts/<ID>/test` → verifica que la cámara alcanza el host.
   Devuelve `errorCode 151` / `connect server fail` si no logra abrir el TCP. **Solo prueba la
   conexión**: no genera un POST que el listener registre, así que un `test` exitoso no significa
   que ya vayan a llegar eventos.
5. Para **conteo de personas no hace falta trigger**: basta `Counting.enabled=true` y el httpHost.
   Verificado en un DS-2XM6825G0 — en su lista de `/ISAPI/Event/triggers` **no existe** ninguno de
   tipo `counting`, y los eventos llegan igual. Para el **tamper** sí hay trigger
   (`eventType: tamperdetection`) y en ese equipo su `notificationMethod` es `center`, no `HTTP`;
   no pudimos confirmar si `center` empuja al httpHost (ver "Discrepancias", punto 4).

**Arming mode** — alternativa no usada aquí: `GET /ISAPI/Event/notification/alertStream` deja una
conexión persistente `multipart/mixed` por la que la cámara escribe eventos y latidos. Un latido
se reconoce por `eventType=videoloss` + `eventState=inactive`. Sería un rediseño (cliente
persistente en vez de servidor), con la ventaja de detectar la caída del enlace por timeout de
latido en vez de con el ping HTTP actual.

## Habilitar el conteo (§2)

1. `GET /ISAPI/System/Video/capabilities` → `isSupportCounting` debe ser `true`.
2. `GET /ISAPI/System/Video/inputs/channels/<ID>/counting/capabilities` → parámetros soportados.
3. `PUT /ISAPI/System/Video/inputs/channels/<ID>/counting` con `<Counting><enabled>true</enabled>`
   (`XML_Counting`; `<ID>` es el canal de video, típicamente `1`).

Conteo de niños (§3): `<ChildFilter><enabled>true` en el mismo `XML_Counting`; los resultados
llegan en `<childCounting>` del evento — nodo que el repo ya parsea pero **no usa**.

El índice completo de endpoints con método y mensaje está en
[references/endpoints.md](references/endpoints.md).

## Diagnóstico: no llegan eventos

En orden, del lado nuestro hacia la cámara:

1. ¿El proceso escucha? `ss -lntp | grep 8088`
2. ¿`Content-Type` aceptado? Desde **1.0.30** el handler acepta cualquier media type que contenga
   `xml` y **deja WARN al descartar**, así que un `multipart/form-data` —que la spec §6.2 permite
   cuando la cámara adjunta imagen— ya no desaparece sin rastro. Sigue sin parsearse; ver
   "Discrepancias".
3. ¿Llega el POST? Corre con `-logStd -debug -logxml` y revisa `cameralog`: registra el cuerpo
   crudo antes de parsear ([listenner.go:56](../../../peoplecounting/listenner.go#L56)).
4. ¿El httpHost está bien y alcanzable? `POST .../httpHosts/<ID>/test` → `errorCode 0` / `ok` si
   la cámara alcanza al gateway, `errorCode 151` / `connect server fail` si no. **Es la prueba que
   distingue "la cámara no manda" de "manda y no llega".** Ojo: el rechazo de TCP se ve idéntico a
   un bloqueo de firewall. Y la cámara admite varios destinos (`/1`, `/2`, `/3`), así que agregá
   el gateway en un slot libre en vez de sobrescribir el que ya está.
5. ¿Hay ruta de vuelta? La cámara puede ser alcanzable **desde** el gateway y no poder alcanzarlo
   ella. Para aislarlo, apuntá el httpHost a un tercero vivo en la red destino: si también falla,
   es ruteo; si solo falla el listener, es el firewall o el proceso.
6. ¿El conteo está habilitado? `GET .../counting/status` → `status` debe ser `counting`
   (`counting,stopped,paused`).
7. ¿La IP de origen es la esperada? De ella depende el `id` de puerta (ver CLAUDE.md).

## Contrato del evento entrante

`XML_EventNotificationAlert_PeopleCountingEventMsg` (§9.10). Lo que el repo consume:
raíz `EventNotificationAlert` → `dateTime`, `eventType`, y según el tipo `peopleCounting` /
`childCounting` ([peoplecounting/xml.go](../../../peoplecounting/xml.go)).

Campos de `<peopleCounting>` según la spec: `statisticalMethods`, `RealTime/time`,
`TimeRange/startTime`+`endTime`, `enter`, `exit`, `pass`, `duplicatePeople`.
`enter`/`exit` son **acumulados**, no incrementos.

`statisticalMethods` tiene **tres** valores: `realTime`, `timeRange` y `signalTrigger`
(disparado por entrada de alarma). Los dos últimos traen `<TimeRange>` y son resúmenes de
período, no eventos en vivo.

Tipos de evento manejados hoy ([events.go](../../../peoplecounting/events.go)):
`PeopleCounting`, `scenechangedetection` (cambio brusco de escena) y `shelteralarm`
(video tampering — en `ISAPI_general.pdf` aparece como `tamperDetection`/`shelteralarm`).
Cualquier otro `eventType` produce `result == nil` **sin error** y se ignora en silencio.
Agregar un tipo toca tres archivos: `events.go` (constante + struct), `xml.go` (rama del switch)
y `listen-actor.go` (manejo).

## Verificado contra equipo real

Cámara **DS-2XM6825G0/C-IVS**, firmware **V5.5.850**, medido el 2026-08-12 capturando el push HTTP
con `scripts/capture-events.go`. Estos son hechos observados, no lectura del PDF:

- **El push usa `Content-Type: application/xml; charset="UTF-8"`** — pasa el filtro actual del
  handler. El `text/xml` que documenta §6.2 no es lo que envía este firmware.
- **Namespace del push: `urn:psialliance-org` versión 1.0**, el mismo del fixture del repo. Ojo:
  las respuestas de los GET ISAPI y el `alertStream` usan `http://www.hikvision.com/ver20/XMLSchema`,
  y el PDF documenta `http://www.isapi.org/ver20/XMLSchema`. **Tres namespaces distintos en un solo
  equipo**: no ates ningún parser a uno.
- **`dateTime` llega como `2026-08-12T11:50:47-5:00`**, con el offset sin cero inicial.
  `time.Parse(time.RFC3339, ...)` lo **rechaza**. El regex de `parseDateTime`
  ([listen-actor.go:67](../../../client/listen-actor.go#L67)) es indispensable: sin él se
  descartaría *todo* evento de conteo.
- **`realTime`**: un evento por cruce, valores **acumulados**, delta observado = 1
  (17→17, 17→18, 18→19…). El umbral `diff < 10` tiene margen enorme en este régimen.
- **`timeRange`**: llega cada `dataUploadCycle`, que está en **MINUTOS** (15 por defecto,
  `opt="1,5,10,15,20,30,60"`), y sus `enter`/`exit` son el conteo **de la ventana, no acumulados**
  (p. ej. `enter=1, exit=2` para 11:15→11:30 mientras el acumulado real era 17/15).
- **La IP de origen se preserva** con ruteo puro (`RemoteAddr` = IP de la cámara). Con NAT o
  port-forward se perdería, y el `id` de puerta depende de eso.
- `doorStatus` = **`N_A`**: requiere `countingType=alarmInputTrigger` con la puerta cableada a la
  entrada de alarma de la cámara. Hoy `countingType=none`.
- `tamperDetection.enabled=true` y el trigger `tamper-1` existe con
  `notificationMethod=center`, `notificationRecurrence=beginning`.
- httpHosts: `hostNumber=3`, `httpAuthenticationMethod opt="none,base64"` (no admite digest).
- `isSupportPicture=true`, `uploadImagesDataType opt="URL,binary"`.
- Hora: `timeMode=NTP`, `timeZone=CST+5:00:00`, `timeMode opt="NTP,manual"`, un servidor
  (`ntp2.inm.gov.co:123`) con `synchronizeInterval=1500` **minutos**, alcanzable
  (`errorCode 0`, `ok`). Deriva medida contra el equipo de desarrollo: **−0.8 s**. El `localTime`
  de `GET /ISAPI/System/time` sí viene bien formado (`-05:00`), a diferencia del push de eventos.
- El paquete [isapi/](../../../isapi/) fue validado contra este equipo: `GetDeviceInfo`, `GetTime`,
  `GetTimeCapabilities`, `GetNTPServers` y `TestNTPServer` devuelven lo esperado, y una clave
  incorrecta produce `ErrUnauthorized` sin bloquear la cuenta (un solo intento).

## Discrepancias entre la spec y el código, con su estado real

El estado indica si se manifiesta hoy en el equipo verificado:

1. **`Content-Type: text/xml` se descartaría** — *no ocurría en este firmware* (envía
   `application/xml`), **corregido en 1.0.30**: el filtro acepta cualquier media type con `xml` y
   el descarte deja WARN. Antes era **silencioso con HTTP 200**, el fallo más difícil de
   diagnosticar que tenía el binario.
2. **`multipart/form-data` no se parsea** — *latente y posible en este modelo*
   (`isSupportPicture=true`). Habilitar el envío de imagen rompería el conteo; desde 1.0.30 al
   menos queda un WARN nombrando el `Content-Type`.
3. **`signalTrigger` se contaría como realtime** — *latente, a un cambio de configuración*
   (`countingType` admite `none,alarmInputTrigger` y está en `none`), **corregido en 1.0.30**.
   La consecuencia era peor de lo que parece: como esos eventos traen conteo **de ventana** y no
   acumulado (medido: `enter=1` mientras el acumulado real era 17), `CountingActor` calcularía un
   delta negativo grande, entraría por la rama `diff < 0`, sumaría el valor de ventana si es `< 4`
   y dejaría `rawXmap` en ese valor pequeño — con lo que el **siguiente** evento real daría un
   delta enorme y también se descartaría. No era duplicación: era una cascada que corrompe el
   acumulado. Por eso el filtro se hizo con lista negra (`timeRange` + `signalTrigger`) y no con
   lista blanca: `statisticalMethods` es opcional en ISAPI y exigir `realTime` descartaría todo
   evento de una cámara que omita el nodo.
4. **El tamper se contaría dos veces por episodio** — *no ocurre*: el trigger tiene
   `notificationRecurrence: beginning`, una sola notificación. **Y quedó sin verificar el camino
   completo**: no se logró provocar la detección, así que no sabemos si un `notificationMethod`
   de solo `center` empuja al httpHost o solo alimenta el `alertStream`. Si no empuja, el manejo
   de tamper del binario sería código muerto en este equipo. Es lo primero a medir la próxima vez
   que haya acceso físico a una cámara.

## `doorStatus`: no es la oportunidad que parecía

`GET .../counting/status` devuelve `XML_CountingStatus` con `status` y `doorStatus`
(`open,close,N_A`), y sería un respaldo del estado de puerta que hoy solo llega por MQTT. Pero en
el equipo verificado devuelve **`N_A`**, porque exige `countingType=alarmInputTrigger` con la
puerta **cableada a la entrada de alarma de la cámara**. No es software: es instalación. Vale
tenerlo presente para vehículos nuevos, no como arreglo del gating actual.

## Al agregar comandos salientes

Ya hay dos actores que hablan con la cámara y conviene mirarlos antes de escribir un tercero:
`CameraActor` ([client/camera-actor.go](../../../client/camera-actor.go)) mantiene la hora y el
NTP, y `VideoActor` extrae los clips usando `isapi.HasCoverage` como guarda. Los dos siguen las
reglas de abajo.

- Paquete nuevo (p. ej. `isapi/`), no dentro de `client`: `client` es lógica de actores, no
  transporte.
- Actor dedicado hijo de `counting` siguiendo `actor-conventions`; las llamadas HTTP en goroutine
  con `context` cancelable, **nunca** dentro de `Receive`.
- **Escribí solo lo que difiere.** `CameraActor` compara antes de hacer PUT: sin eso serían
  escrituras periódicas a toda la flota para dejar todo igual.
- **`statusCode 7` es "aplicado, falta reiniciar", no un fallo.** El cliente lo devuelve como
  error porque `ResponseStatus.OK()` solo acepta 0 y 1. `CameraActor` lo interpreta y **sí reinicia
  la cámara**, en el mismo ciclo que escribió: un cambio que responde 7 queda guardado pero inerte
  y la cámara reporta el valor guardado, así que separar el PUT del reinicio deja un estado donde
  la configuración y la lectura del API coinciden en decir algo que la cámara no está haciendo.
  El único freno es `-encoderMinUptime` (30 min), que impide que un binario en bucle de
  supervisión reinicie cámaras.
- **Un `statusCode 1 OK` NO garantiza que el valor quedó.** Medido: pedirle 30 fps, que no está
  entre los admitidos, responde OK y guarda 24 **en silencio**. Sin releer, el ciclo siguiente
  vuelve a ver la diferencia y escribe otra vez, para siempre. Releé después de escribir y volvé a
  medir; si no quedó, ERROR y dejá de intentar en esa cámara.
- **Un 401 tiene que apagar esa cámara hasta el próximo arranque**, no reintentarse cada ciclo:
  el firmware verificado no informa los intentos restantes y bloquea el usuario.
- Timeout explícito en el `http.Client`. El `http.Get` del ping actual no tiene ninguno.
- `defer resp.Body.Close()` siempre.
- **Valida el cuerpo, no solo el código HTTP**: la respuesta es `XML_ResponseStatus` con
  `statusCode` (`0,1`=OK, `2`=Device Busy, `3`=Device Error, `4`=Invalid Operation,
  `5`=Invalid XML Format, `6`=Invalid XML Content, `7`=Reboot Required, `9`=Additional Error) y
  un `subStatusCode` descriptivo. Un 200 con `statusCode` de error es posible.
- Toda escritura (`PUT`/`POST` de configuración, `reboot`) tiene efecto en campo: idempotente,
  registrada en `infoLog` con el resultado, y sin reintento automático sin límite.
