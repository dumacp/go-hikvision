# Cómo se identifica un clip

Es la parte del contrato que más fácil se implementa mal, porque hay una trampa: **el
nombre del archivo `.mp4` lleva un `event_id`, y ese id no es el único paso que contiene.**

---

## La llave es `event_id`

Ocho hexadecimales, generados al contar el paso. El mismo valor viaja por cuatro lugares:

```
COUNTERSDOOR       value.event_id        ← "conté un pasajero, se llama ce9f5d9d"
COUNTERSDOORVIDEO  value.event_id        ← "el clip de ce9f5d9d ya está"
                   value.clip            ← ...y se llama así
                   value.metadata        ← ...y su JSON así
JSON (sidecar)     event_id              ← el mismo paso
                   clip                  ← el .mp4 que lo contiene
nombre de archivo  ..._<event_id>.mp4    ← ⚠️ CUIDADO, ver abajo
                   ..._<event_id>.json
```

---

## ⚠️ La trampa: varios pasos comparten un clip

**Un `.mp4` puede contener varios cruces, y su nombre lleva el `event_id` de uno solo** — el
primero de la ráfaga, el que definió la ventana.

Ejemplo real de tres personas cruzando seguidas:

```
20260827T140049_FLO-W7-0036_p1_9b7f5edf.mp4          ← UN clip
20260827T140049_FLO-W7-0036_p1_in_9b7f5edf.json      ← paso 1, offset_s 10
20260827T140051_FLO-W7-0036_p1_in_a3c81e04.json      ← paso 2, offset_s 12
20260827T140053_FLO-W7-0036_p1_out_7d2f9b11.json     ← paso 3, offset_s 14
```

Los tres JSON dicen `"clip": "20260827T140049_FLO-W7-0036_p1_9b7f5edf.mp4"`.

Y se publican **tres** `COUNTERSDOORVIDEO`, uno por paso, los tres con el mismo `clip`:

```json
{"...","event_id":"9b7f5edf","clip":"20260827T140049_FLO-W7-0036_p1_9b7f5edf.mp4", ...}
{"...","event_id":"a3c81e04","clip":"20260827T140049_FLO-W7-0036_p1_9b7f5edf.mp4", ...}
{"...","event_id":"7d2f9b11","clip":"20260827T140049_FLO-W7-0036_p1_9b7f5edf.mp4", ...}
```

### Lo que hay que hacer

> **Para saber qué clip corresponde a un paso, leé el campo `clip`** del
> `COUNTERSDOORVIDEO` o del JSON. **Nunca lo deduzcas parseando el nombre del `.mp4`.**

Buscar el archivo cuyo nombre termine en el `event_id` del paso **funciona solo para el
primero de cada ráfaga**. Para los demás no existe ningún `.mp4` con su id, y el paso
aparecería como "sin video" aunque el video exista y lo muestre.

### Por qué está hecho así

Sin agrupar, una ráfaga de 10 pasos producía **10 clips y 5.5 MB, la mitad byte a byte
idénticos**: la cámara ajusta el inicio al keyframe anterior, así que dos eventos del mismo
GOP devuelven los mismos bytes. Agrupando quedan **3 clips y 1.4 MB**, y la cola drena en
49 s en vez de 112.

En un vehículo con datos celulares esa diferencia es la que hace la solución viable.

---

## Las dos direcciones del cruce

### Desde el conteo hacia el video

```
1. llega COUNTERSDOOR        → se registra el paso con su event_id
2. llega COUNTERSDOORVIDEO   → mismo event_id, y ahí están los nombres de archivo
3. se buscan en el bucket    → <prefijo>/<día>/<equipo>/<clip>
```

**El paso 2 puede no llegar nunca.** Eso significa, sin ambigüedad, que ese paso no tiene
video: no hubo grabación de ese instante, faltó espacio o la cámara no respondió.

**El paso 3 puede tardar.** Por datos celulares con política `notmetered` solo viajan los
JSON; los `.mp4` esperan al patio. El `COUNTERSDOORVIDEO` dice que el clip está **en el
gateway**, no en el bucket.

### Desde un archivo hacia el conteo

```
1. del nombre del .json       → event_id, equipo, puerta, tipo
2. o mejor, del contenido     → event_id, door, type, event_time, clip
3. se busca el COUNTERSDOOR con ese event_id
```

El JSON es **autosuficiente**: trae todo lo necesario sin depender de la ruta ni del nombre.
Eso es deliberado, porque en el repositorio final la estructura de directorios se puede
perder.

---

## Reconstruir la ruta completa

El `COUNTERSDOORVIDEO` y el JSON traen el nombre del archivo **sin ruta**. Para armar la
ruta en el bucket:

```
<prefijo>/<día>/<equipo>/<clip>
```

- **prefijo** — configuración del despliegue, no viaja en el evento
- **día** — los primeros 8 caracteres del nombre, `20260827` → `2026-08-27`
- **equipo** — `value.gateway_hostname` del evento, o del nombre del archivo
- **clip** — `value.clip` tal cual

El día **también** se puede sacar de `event_time` del JSON, y da el mismo resultado: los dos
vienen del mismo reloj de cámara.

---

## Cuando `event_id` no está

Hay dos casos en los que el `COUNTERSDOOR` llega **sin** `event_id`:

| Caso | |
|---|---|
| `-withoutEventID` activo | compuerta de compatibilidad; el campo se omite siempre |
| evento replicado de una base anterior a 1.0.32 | no tiene uid que publicar |

En los dos, **el cruce conteo↔video es imposible desde el lado del conteo**. El
`COUNTERSDOORVIDEO` y los archivos se siguen generando con su `event_id` normal, pero no hay
con qué unirlos al paso.

`-withoutEventID` existe para operar mientras la plataforma no procese el campo. Se quita
corriendo el mismo binario sin esa opción — no hay que recompilar.

---

## Resumen para implementar

```
✔  unir por  value.event_id
✔  el nombre del clip se lee de  value.clip  o del JSON
✔  varios event_id pueden apuntar al MISMO clip
✔  cada event_id tiene SU propio .json
✔  offset_s dice en qué segundo del clip está ese cruce

✘  NO deducir el clip parseando el event_id del nombre del .mp4
✘  NO asumir que llega un COUNTERSDOORVIDEO por cada COUNTERSDOOR
✘  NO asumir que el .mp4 ya está en el bucket cuando llega el evento
✘  NO asumir que el día de la ruta es la fecha de subida
```
