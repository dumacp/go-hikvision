# El repositorio de clips

Qué queda en el bucket, con qué ruta y qué hay dentro de cada archivo.

Los archivos los **escribe** `go-hikvision` en el disco del gateway y los **sube**
`clipscamera`. La ruta en el bucket la arma el segundo a partir de lo que dejó el primero.

---

## La ruta

```
gs://clips-flota-test/flota-test/2026-08-27/FLO-W7-0036/20260827T140049_FLO-W7-0036_p1_9b7f5edf.mp4
   └──── bucket ────┘└─ prefijo ┘└── día ──┘└─ equipo ─┘└──────────── archivo ────────────────┘
```

| Elemento | De dónde sale | |
|---|---|---|
| **bucket** | `-gcsBucket` | acepta con o sin `gs://` |
| **prefijo** | `-gcsPrefix` | **opcional**; vacío escribe en la raíz del bucket |
| **día** | nombre del subdirectorio que creó `go-hikvision` | `YYYY-MM-DD` |
| **equipo** | se lee del **nombre del archivo** | hostname del gateway |
| **archivo** | el `.mp4` o el `.json` | ver abajo |

### El prefijo

Separa flotas o entornos dentro de un mismo bucket, y permite acotar el permiso de la
credencial a ese prefijo del lado de GCS. Si no se pasa, la ruta empieza directo en el día.

### El día — ojo con esto

**Sale del reloj de la cámara, no de la fecha de subida.** `go-hikvision` crea el
subdirectorio con la hora local del evento, que es la hora que reporta la cámara, y
`clipscamera` la reproduce tal cual en el destino.

Consecuencia: si el reloj de la cámara está corrido, **el clip queda archivado en el día
equivocado**, de forma coherente con su nombre y con el `event_time` del JSON, pero no con
la hora real. Un clip subido hoy puede aterrizar en la carpeta de hace dos semanas.

### El equipo

**Se lee del nombre del archivo, no del hostname del proceso que sube.** Los dos coinciden
mientras nadie renombre el gateway; si se renombra con clips pendientes en la cola, el
hostname nuevo contradiría al nombre del archivo. Leyéndolo del nombre, la ruta y el archivo
nunca se contradicen.

Si el nombre no tiene la forma esperada, se cae al hostname del proceso; y si tampoco hay,
el archivo se sube **al día pelado**, sin carpeta de equipo. Un clip en el lugar equivocado
vale más que ninguno.

### Un día puede recibir archivos de varias corridas

La ruta no tiene nada que identifique una ejecución o un lote. Varias carpetas de origen
distintas pueden converger en el mismo `<día>/<equipo>/` sin colisionar, porque el nombre de
cada archivo ya es único. Verificado: 93 archivos de tres directorios distintos cayeron en
el mismo día sin una sola colisión.

---

## Los archivos

Por cada ventana de video se suben **un `.mp4` y uno o más `.json`**.

```
20260827T140049_FLO-W7-0036_p1_9b7f5edf.mp4          ← el clip
20260827T140049_FLO-W7-0036_p1_in_9b7f5edf.json      ← un paso que ocurre en ese clip
20260827T140056_FLO-W7-0036_p1_out_8b776d87.json     ← otro paso, el MISMO clip
```

### Forma del nombre

```
clip      <YYYYMMDD>T<HHMMSS>_<equipo>_p<puerta>_<event_id>.mp4
json      <YYYYMMDD>T<HHMMSS>_<equipo>_p<puerta>_<in|out>_<event_id>.json
```

| Parte | |
|---|---|
| `<YYYYMMDD>T<HHMMSS>` | hora local **del reloj de la cámara** |
| `<equipo>` | hostname del gateway; **puede contener guiones bajos** |
| `p<puerta>` | `p0` delantera, `p1` trasera |
| `<in\|out>` | **solo en el JSON**: el tipo pertenece al evento, no al clip |
| `<event_id>` | ocho hexadecimales |

> **El `.mp4` no lleva `in`/`out` a propósito.** Un clip es de una puerta y una ventana de
> tiempo, y puede contener entradas y salidas a la vez. El tipo pertenece al paso, o sea al
> JSON.

> **Para extraer el equipo del nombre, delimitá por el campo `p<puerta>`, no tomes el
> segundo campo.** Un hostname con guión bajo —`NE_RCX_0000`— se partiría por la mitad.

Si dos archivos fueran a llamarse igual, el segundo recibe un sufijo `_2`, `_3`… antes de la
extensión. No es lo normal; es la red de seguridad.

### El archivo de video

| | |
|---|---|
| Extensión | `.mp4` |
| Contenedor | MP4 |
| Codec | **`H.264` o `H.265`**, según lo que la cámara entregue para ese tramo |
| `Content-Type` al subir | `video/mp4` |
| Tamaño típico medido | 200 KB – 750 KB, promedio **~453 KB** |

**El codec no es fijo ni es necesariamente el que la cámara tiene configurado hoy.** La SD
conserva grabaciones de antes de un cambio de codec, así que clips del mismo día pueden
venir en formatos distintos. **El codec real de cada clip está en el campo `codec` de su
JSON** — leelo de ahí, no lo asumas.

Sin audio: se extrae solo la pista de video.

### El archivo de datos (sidecar)

| | |
|---|---|
| Extensión | `.json` |
| `Content-Type` al subir | `application/json` |
| Tamaño típico | ~580 bytes |

---

## Contenido del JSON

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
  "fps_effective": 20.08,
  "codec": "H.265"
}
```

| Campo | Qué es |
|---|---|
| `event_id` | la llave con la plataforma. **El mismo** que trae el `COUNTERSDOOR` |
| `gateway_hostname` | equipo donde corrió el binario |
| `door` | `0` delantera, `1` trasera |
| `type` | `in` o `out` |
| `event_time` | hora del cruce, con huso horario local |
| `event_time_ms` | el mismo instante en milisegundos Unix UTC |
| `clip` | **nombre del `.mp4` que contiene este cruce**, sin ruta |
| `clip_start_requested` | el instante que se le **pidió** a la cámara |
| `offset_s` | en qué segundo del clip ocurre este cruce |
| `duration_s` | duración pedida del clip, en segundos. **No es siempre la misma**: un paso solo da la ventana base (16 s), pero los pasos seguidos de una puerta se encadenan en un clip que crece hasta 60 s. Leé el campo, no asumas un valor |
| `camera` | IP de la cámara |
| `camera_serial` | identidad estable del equipo |
| `camera_mac` | MAC de la cámara |
| `camera_counter` | **incremento de este evento**, casi siempre `1` |
| `samples` | cantidad de fotos del clip |
| `bytes` | tamaño del `.mp4` |
| `max_gap_s` | hueco más largo entre dos fotos consecutivas |
| `fps_effective` | fotos por segundo reales del clip |
| `codec` | `H.264` o `H.265`, el de **este** clip |

### Precisiones que evitan leer de más

**`event_time_ms` va en milisegundos pero la resolución de origen es de un segundo.** La
cámara envía la hora sin fracción, así que **siempre termina en `000`**. No hay precisión de
milisegundos que leer.

**`clip_start_requested` es el instante pedido, no el primer frame.** La cámara posiciona la
reproducción en el keyframe anterior, así que el video puede empezar hasta un GOP antes —
medido: 2 s a 20 fps en un modelo y 4.2 s en otro. Da más pre-roll que el configurado, nunca
menos. **`offset_s`, en cambio, se mide contra el primer frame real del clip** siempre que el
binario lo conoce —en los clips bajados por descarga HTTP, que es como se extrae el modelo
nuevo— y entonces es exacto. En los extraídos por RTSP se mide contra el instante pedido y
puede quedarse corto hasta un GOP: el cruce aparece unos segundos **después** del
`offset_s`, nunca antes.

**`camera_counter` es el incremento, no el acumulado.** Cuando es mayor que `1`, el evento
cuenta varios pasajeros y el clip puede no mostrarlos a todos — pasa cuando el binario
estuvo un rato sin recibir eventos. El conteo sigue siendo correcto; lo que no alcanza es el
video.

**`camera_serial` y `camera_mac` pueden venir vacíos.** Se consultan una vez por cámara; si
esa consulta falla, los campos quedan vacíos pero **la extracción sigue**: el clip vale más
que su metadato.

### `max_gap_s` dice si el clip sirve, sin abrirlo

A 20 fps lo normal es un hueco de **0.05 s**. Medido en la misma cámara y la misma escena:

| | fotos en 12 s | hueco máximo | tamaño |
|---|---|---|---|
| H.264+ apagado | 241 | **0.083 s** | 718 KB |
| H.264+ encendido | 2 | **12.2 s** | 85 KB |

Con H.264+ (SmartCodec) y escena quieta, **el clip entero es un hueco**: pesa 8 veces menos
y no se ve el paso. Comparando `max_gap_s` contra `duration_s` la plataforma puede detectar
de lejos una cámara mal configurada, sin descargar el video.

---

## Lo que el repositorio NO garantiza

**No hay garantía de orden.** El `.json` y el `.mp4` del mismo paso se suben por separado, y
el JSON —que pesa 580 bytes contra 450 KB— suele llegar primero. Con la política de enlace
`notmetered`, por datos celulares **solo viajan los JSON** y los videos esperan al patio: el
clip puede tardar horas en aparecer.

**No todo paso tiene clip.** Si no hubo grabación de ese instante, faltó espacio en disco o
la cámara no respondió, el `COUNTERSDOORVIDEO` no se publica y los archivos no existen. El
`COUNTERSDOOR` sí salió.

**Un archivo puede subirse dos veces sin duplicarse.** Los objetos se crean con precondición
"solo si no existe": un reintento devuelve `412` y se cuenta como éxito. Verificado — seis
corridas del mismo directorio dejaron un solo objeto por archivo, con la generación sin
cambiar.

**Los archivos se borran del gateway por retención**, no por haberse subido. Lo ya enviado
se mueve a `enviados/` y se borra al cumplir el plazo; lo **no** enviado también se borra al
cumplirlo, y eso queda en el log como `borrados SIN ENVIAR`.
