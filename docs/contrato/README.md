# Contrato con la plataforma

Qué publica el conteo de pasajeros por MQTT y qué deja en el repositorio de clips. Es la
documentación de las dos fronteras que cruza el dato: **los eventos** que consume la
plataforma y **los archivos** que quedan en el bucket.

| | |
|---|---|
| [eventos-mqtt.md](eventos-mqtt.md) | todos los mensajes, cuáles son nuevos y a cuáles se les agregaron campos |
| [repositorio-clips.md](repositorio-clips.md) | la ruta en el bucket, los archivos y el contenido del JSON |
| [correlacion.md](correlacion.md) | **cómo se identifica un clip** desde un evento y desde el JSON |

Si vas a implementar del lado de la plataforma y solo podés leer uno, leé
[correlacion.md](correlacion.md): ahí está la trampa que hace perder tiempo.

---

## Los dos binarios

El trabajo está partido en dos procesos que corren en el gateway del vehículo y **no se
hablan entre sí** salvo por un aviso de MQTT y por el directorio de archivos.

```
┌───────────────┐   eventos HTTP/XML   ┌──────────────┐
│    cámara     │ ───────────────────► │ go-hikvision │
│  Hikvision    │ ◄─────────────────── │              │
└───────────────┘   RTSP (recorta)     └──────┬───────┘
                                              │
                        MQTT local            │   /SD/video/<día>/<archivos>
                     127.0.0.1:1883 ◄─────────┤
                                              ▼
                                       ┌──────────────┐
                                       │ clipscamera  │ ──► bucket
                                       └──────────────┘
```

**`go-hikvision`** cuenta los pasajeros, publica los eventos y recorta el video de cada
paso dejándolo en disco. **`clipscamera`** recorre ese directorio y sube los archivos.

La división importa para leer este contrato: los eventos salen del primero, los archivos
los pone el primero y los sube el segundo, y **ninguno de los dos garantiza el orden en que
llegan las dos cosas**. Un clip puede estar en el bucket antes o después de que la
plataforma procese el conteo que le corresponde.

---

## Qué cambió y desde cuándo

| Versión | Cambio |
|---|---|
| **1.0.32** | aparecen `CAMERATIME` y `COUNTERSDOORVIDEO`; `COUNTERSDOOR` gana `event_id` |
| 1.0.37 | `CAMERATIME` gana `video_codec`, `video_fps`, `smart_codec` |
| 1.0.38 | `CAMERATIME` gana `video_dir`, `video_dir_free_mb` y los estados `videodir`/`videodir_ok` |
| 1.0.39 | `-withoutEventID` permite volver al `COUNTERSDOOR` previo a 1.0.32 |
| 1.0.40 | `storage` puede traer el valor `nooverwrite` |\n| sin publicar | aparece `CAMERAPOWER`: la cámara se apaga con el vehículo (`-cameraPowerGPIO`) |

**`event_id` es el único campo que se agregó a un mensaje que ya existía.** Verificado
comparando el conjunto de etiquetas `json:` contra la versión anterior, y confirmado
después generando el JSON con las dos versiones compiladas y comparándolo byte por byte: con
`-withoutEventID` la salida es **idéntica** a la de 1.0.29, incluido el `TAMPERING`.

Todo lo demás son mensajes **nuevos**, en un **tópico distinto** del que consumía la
plataforma. Un consumidor de `EVENTS/backcounter` no los ve.
