---
name: build-release
description: Compilar, versionar y preparar el binario go-hikvision para el gateway embebido — cross-compile, dependencias por replace en el filesystem, regeneración de protobuf y checklist de release. Úsala al preparar un despliegue, subir la versión, o cuando la compilación falla por dependencias.
---

# Build y release de go-hikvision

## Compilación local

```bash
go build ./... && go vet ./...
go build -o /tmp/hikvision ./client/main
/tmp/hikvision -version     # imprime y sale con código 2 (no es error)
```

`go test ./...` **falla hoy** por un caso de prueba incompleto en `peoplecounting`
(ver deuda conocida en CLAUDE.md). No lo tomes como regresión de tu cambio. Los tres paquetes
con pruebas reales sí pasan, así que corré esos en vez del global:

```bash
go test ./client/... ./isapi/
```

## Dependencias por `replace` — la causa #1 de builds roto

[go.mod](../../../go.mod) resuelve tres módulos desde el filesystem, no desde la red:

```
../go-doors                    → github.com/dumacp/go-doors
../go-actors                   → github.com/dumacp/go-actors
../../asynkron/protoactor-go   → github.com/asynkron/protoactor-go
```

El repo debe estar en `$GOPATH/src/github.com/dumacp/go-hikvision` con los hermanos presentes.
Si el build falla con `module ... not found` o `directory ... does not exist`, verifica primero
esas rutas — no toques `go.mod`. Y ten en cuenta que un cambio en cualquiera de esos tres repos
entra al binario sin quedar registrado en este historial: al diagnosticar un comportamiento raro
en campo, revisa también el estado de los repos hermanos.

## Cross-compile al gateway

El binario corre en un gateway embebido Linux (rutas `/SD/...`, GPIO por sysfs, syslog).
Nada del código usa cgo, así que:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -ldflags="-s -w" \
  -o hikvision-$(grep -oP 'showVersion = "\K[^"]+' client/main/main.go)-g$(git rev-parse --short HEAD)-armv7 \
  ./client/main
```

**`armv7l` está verificado**: el binario corre en un gateway `FLO-W7-0036` con BusyBox 1.23.2,
`/bin/sh` → bash, y `/SD` en `/dev/mmcblk1p1`. Sale estático y sin sección dinámica, así que no
depende de la libc del equipo. Pesa ~14 MB.

**Nombra el artefacto con la versión Y el hash del commit.** La versión sola no identifica un
binario: la constante se sube solo al desplegar, así que varios commits la comparten. En esta
historia hubo tres binarios distintos reportando `1.0.36`, y eso hace inútil cualquier reporte de
pruebas que diga "probamos la 1.0.36".

## Protobuf

Solo si editas `.proto`:

```bash
cd client/messages && ./protobuf.sh    # protoc --go_out con paths=source_relative
```

Requiere `protoc` y `protoc-gen-go` en el PATH. El `.pb.go` generado **se commitea**.

Cambiar `messages.Snapshot` afecta las bases boltdb ya desplegadas en `/SD/boltdbs/countingdb`:
nunca reutilices un número de campo, nunca cambies el tipo de uno existente, y mantén la lectura
tolerante (`GetXMap() != nil`) para que un equipo con base vieja siga recuperándose.
Ver `actor-conventions` para los cuatro puntos que hay que tocar al agregar estado.

## Checklist de release

1. `go build ./... && go vet ./...` limpios, y `go test ./client/... ./isapi/`.
2. **NO subas `showVersion`.** Se sube aparte, cuando hay acuerdo de que lo que está en `master`
   es lo que se despliega, y el número lo decide quien despliega. Un commit que la sube por cada
   cambio produce versiones que nunca existieron como binario: en esta historia `1.0.35` es una
   sintaxis que se revirtió en `1.0.36`.
3. Probar sin hardware con la skill `simulate-camera-events`.
4. Si cambió el contrato MQTT o el XML de la cámara, actualizar `mqtt-contract` /
   `hikvision-isapi` en el mismo commit.
5. Commit describiendo el cambio y **por qué**, con las mediciones que lo respaldan. El historial
   de este repo se usa para entender decisiones seis meses después, no solo para saber qué cambió.
6. Cross-compile con el nombre que lleva versión y hash.

## Comportamiento en el equipo que conviene conocer

- Sin `-logStd` todo va a **syslog** (`journalctl` / `/var/log/messages`), con `-logxml` el XML
  crudo va a `/SD/logs/camera*` rotando en 2 MB × 20 archivos.
- Arranque: espera al broker MQTT local; si no está, entra en panic y la supervisión reinicia.
  Es esperado si el binario arranca antes que mosquitto.
- `-pathdb` en `/SD/boltdbs/countingdb` guarda los acumulados. **Borrarlo reinicia los
  contadores del vehículo**: no lo hagas como "limpieza" de diagnóstico sin acordarlo.
- Publica registros cada 45 s y hace snapshot cada 10 eventos persistidos.
