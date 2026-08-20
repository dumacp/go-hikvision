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
(ver deuda conocida en CLAUDE.md). No lo tomes como regresión de tu cambio: compara antes
y después.

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
  go build -ldflags="-s -w" -o hikvision-1.0.29-armv7 ./client/main
```

**Confirma la arquitectura destino con el usuario antes de entregar** — el repo no tiene script
de build ni CI que la fije, y `arm/GOARM=7` es la suposición razonable para esta familia de
equipos, no un dato verificado. En el equipo: `uname -m`.

Nombra el artefacto con la versión: facilita la trazabilidad de qué binario quedó en cada
vehículo, que es información que hoy no existe en ningún otro lado.

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

1. `go build ./... && go vet ./...` limpios.
2. Subir `showVersion` en [client/main/main.go](../../../client/main/main.go#L19).
   Es la única fuente de versión y lo que se ve en el log de arranque y por MQTT.
3. Probar sin hardware con la skill `simulate-camera-events`.
4. Si cambió el contrato MQTT o el XML de la cámara, actualizar `mqtt-contract` /
   `hikvision-isapi` en el mismo commit.
5. Commit con mensaje al estilo del historial (`version 1.0.29`, o descripción del fix).
6. Cross-compile con la arquitectura confirmada y nombre versionado.

## Comportamiento en el equipo que conviene conocer

- Sin `-logStd` todo va a **syslog** (`journalctl` / `/var/log/messages`), con `-logxml` el XML
  crudo va a `/SD/logs/camera*` rotando en 2 MB × 20 archivos.
- Arranque: espera al broker MQTT local; si no está, entra en panic y la supervisión reinicia.
  Es esperado si el binario arranca antes que mosquitto.
- `-pathdb` en `/SD/boltdbs/countingdb` guarda los acumulados. **Borrarlo reinicia los
  contadores del vehículo**: no lo hagas como "limpieza" de diagnóstico sin acordarlo.
- Publica registros cada 45 s y hace snapshot cada 10 eventos persistidos.
