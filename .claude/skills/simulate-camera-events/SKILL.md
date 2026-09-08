---
name: simulate-camera-events
description: Probar el binario go-hikvision sin cámara física — inyectar eventos XML de people counting y tamper al listener HTTP con el script incluido, y qué flags hacen falta para que el conteo no se descarte. Úsala para verificar cambios en el parser, en ListenActor, en CountingActor o en los payloads MQTT antes de desplegar al vehículo.
---

# Simular eventos de cámara en local

Reproduce el `POST` que hace la Hikvision contra el servidor de
[peoplecounting/listenner.go](../../../peoplecounting/listenner.go), sin hardware.

## Arrancar el binario para pruebas

```bash
go run ./client/main -logStd -debug -logxml \
  -socket :8088 -pathdb /tmp/countingdb-test \
  -countWithCloseDoor=true
```

Cuatro cosas que **hay que** hacer así o las pruebas no muestran nada:

- **`-countWithCloseDoor=true`** es obligatorio en local. Sin estado de puerta llegando por MQTT,
  `CountingActor` considera la puerta cerrada y descarta cada paso con un WARN. Este flag es
  posicional: la primera aparición configura el `id 0`, que es el que verás desde localhost.
- **`-pathdb` a una ruta desechable**. La default es `/SD/boltdbs/countingdb` (el equipo real) y
  la base guarda los acumulados: reusarla mezcla datos de prueba con producción y falsea los
  deltas. Borra el archivo entre corridas para partir de cero.
- **`-debug`** habilita `buildLog`, donde se ve el estado interno tras cada evento. Sin él no hay
  forma de ver por qué un evento se descartó.
- Necesitas **mosquitto en `127.0.0.1:1883`**. Sin broker, el actor MQTT entra en panic y la
  supervisión reinicia en ciclo. Levántalo con `mosquitto -v` y observa la salida con
  `mosquitto_sub -h 127.0.0.1 -t '#' -v`.

## Inyectar eventos

```bash
# un paso: 5 entradas, 3 salidas (acumulados, como los manda la cámara)
.claude/skills/simulate-camera-events/scripts/send-event.sh counting 5 3

# secuencia realista de acumulados crecientes
for i in 1 2 3 4 5; do
  .claude/skills/simulate-camera-events/scripts/send-event.sh counting $i $i
  sleep 1
done

.claude/skills/simulate-camera-events/scripts/send-event.sh timerange 100 100     # debe descartarse
.claude/skills/simulate-camera-events/scripts/send-event.sh signaltrigger 100 100 # HOY NO se descarta (bug)
.claude/skills/simulate-camera-events/scripts/send-event.sh scenechange           # → TAMPERING
.claude/skills/simulate-camera-events/scripts/send-event.sh shelteralarm          # → TAMPERING
```

El script pone `dateTime` en la hora actual (obligatorio: los eventos con fecha anterior al
último visto se descartan) y el `Content-Type: application/xml` (sin él el handler **ignora el
cuerpo y responde 200 igual**, el fallo más engañoso de todos).

## Inyectar en el gateway ARM: `wget` de BusyBox no hace POST

En el equipo (`BusyBox v1.23.2`) **no hay `curl`, no hay `timeout`, y el `wget` solo hace GET** —
no tiene `--post-data` ni `--post-file`. Hay `nc`, así que el request se arma a mano:

```sh
cat > /tmp/post.sh <<'EOF'
#!/bin/sh
# post.sh <archivo-xml> [host puerto]
f="$1"; t="${2:-127.0.0.1 8088}"
len=$(wc -c < "$f")
{
printf "POST / HTTP/1.1\r\n"
printf "Host: 127.0.0.1\r\n"
printf "Content-Type: application/xml\r\n"
printf "Content-Length: %s\r\n" "$len"
printf "Connection: close\r\n\r\n"
cat "$f"
} | nc $t
EOF
chmod +x /tmp/post.sh
```

Tres cosas que hacen falta para que el evento no se descarte:

- **el `dateTime` tiene que venir del reloj correcto.** Si el reloj del gateway está corrido, generá
  la marca en la máquina de desarrollo y copiá el XML — el evento se compara contra el `timeBefore`
  de esa puerta y contra las grabaciones de la cámara, que usan el reloj de la cámara;
- **el `Content-Type` es obligatorio**: sin `xml` en él el handler descarta el cuerpo y responde 200
  igual (deja WARN desde 1.0.30);
- **para acotar la corrida no uses `timeout`**, que no existe: lanzá en segundo plano y matá por PID
  (`cmd & BGPID=$!; sleep 25; kill $BGPID`).

Y para que la extracción de video se ejercite, el evento desde `127.0.0.1` cae en la **puerta 0**,
así que la cámara tiene que estar en ese índice: `-camera <ip>:0`. Con `-camera <ip>:1` el evento va
a la puerta 0, que queda vacía, y el log dice `sin -camera para la puerta 0, no se puede extraer
video`.

## Qué esperar

| Envío | Resultado correcto |
|---|---|
| `counting` con acumulados crecientes | `messages.Event` INPUT/OUTPUT con el **delta**, publicación en `EVENTS/backcounter` y acumulado en `COUNTERSMAPDOOR` |
| `counting` repetido con el mismo valor | nada (delta 0) |
| `counting` con salto ≥ 10 | descartado, WARN `diff inputs > 10` |
| `counting` con valor menor al anterior | WARN de desviación; solo se cuenta si el valor nuevo es < 4 |
| `timerange` | descartado, WARN `event timeRange` |
| `signaltrigger` | **debería** descartarse (es un resumen de período, igual que `timeRange`) pero hoy se cuenta; ver "Discrepancias" en la skill `hikvision-isapi` |
| `scenechange` / `shelteralarm` | `TAMPERING` en `EVENTS/counterevents` |

Los fixtures cubren los dos formatos que existen en campo: los de `realTime`/`timeRange` usan
namespace ISAPI **v1.0** (`urn:psialliance-org`), igual que el push de las cámaras desplegadas
(verificado); el de `signaltrigger` usa **v2.0** (`http://www.isapi.org/ver20/XMLSchema`) con
`pass` y `duplicatePeople`, como la documentación oficial. El parser ignora namespaces, así que
ambos deben funcionar — y eso es justamente lo que conviene verificar tras tocar `xml.go`.

El script genera `dateTime` con el offset **`-5:00`**, sin cero inicial, porque es lo que emite la
cámara real y `time.Parse(time.RFC3339, ...)` lo rechaza: así el fixture ejercita el regex de
`parseDateTime`, que es el único motivo por el que esos eventos no se descartan. Con
`RFC3339_ESTRICTO=1` se envía el offset canónico (`-05:00`) para comparar.

## Cadencia real de la cámara (medida en un DS-2XM6825G0, V5.5.850)

Útil para saber si una prueba es representativa:

- **`realTime`**: un evento **por cruce**, valores acumulados, delta de 1. Es el que alimenta los
  contadores. Simúlalo incrementando de a uno, no a saltos.
- **`timeRange`**: uno cada `dataUploadCycle` **minutos** (15 por defecto), y sus `enter`/`exit`
  son el conteo **de esa ventana, no acumulados** — números chicos aunque el acumulado sea alto.
  El binario los descarta, y hace bien: tratarlos como acumulados corrompería el estado.
- Los latidos del `alertStream` (`eventType=videoloss`, `eventState=inactive`, cada ~10 s) **no**
  llegan por el push HTTP; solo existen en modo arming.

Recuerda que `COUNTERSMAPDOOR` **no se publica** mientras entradas y salidas sumen 0, y que sale
cada 45 s (o al reiniciar tras un snapshot). Ten paciencia o envía primero un paso válido.

## Límite conocido: el `id` de puerta

El `id` se deriva de la IP de origen: desde localhost siempre da **`id 0`** (puerta frontal).
Probar `id 1` requiere que el POST venga de `192.168.188.21`, que está hardcodeada en
[client/listen-actor.go:85](../../../client/listen-actor.go#L85). Opciones: cambiar
temporalmente esa constante, o hacer que la IP sea configurable — que es la mejora pendiente
real en ese punto (ver deuda conocida en CLAUDE.md).

## Prueba unitaria del parser

[peoplecounting/listenner_test.go](../../../peoplecounting/listenner_test.go) tiene un caso con
XML real pero `want: nil` sin completar, así que `go test ./...` **falla hoy**. Si vas a tocar
`xml.go`, completar ese `want` con el struct esperado es la forma más rápida y barata de blindar
el cambio; los XML de `references/` sirven como casos adicionales.
