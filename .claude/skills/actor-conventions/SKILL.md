---
name: actor-conventions
description: Convenciones para crear o modificar actores protoactor-go en go-hikvision (Logger embebido, ciclo de vida, supervisión con Panic, persistencia con boltdb, jerarquía padre/hijo). Úsala antes de añadir un actor nuevo, cambiar mensajes entre actores, o tocar la persistencia de CountingActor.
---

# Convenciones de actores en go-hikvision

## Plantilla de actor

Todo actor del paquete `client` sigue esta forma. Copia el patrón, no inventes otro:

```go
// XActor actor to <qué hace>
type XActor struct {
    *Logger              // logs inyectados desde main
    ctx     actor.Context // si necesitas ctx fuera de Receive (goroutines)
    // estado propio: mapas ya inicializados por el constructor
}

func NewXActor() *XActor {
    a := &XActor{}
    a.Logger = &Logger{}
    // inicializa TODOS los mapas aquí, nunca en Started
    return a
}

func (a *XActor) Receive(ctx actor.Context) {
    a.ctx = ctx
    switch msg := ctx.Message().(type) {
    case *actor.Started:
        a.initLogs()   // OBLIGATORIO: sin esto los *log.Logger son nil → panic
        a.infoLog.Printf("actor started %q", ctx.Self().Id)
    case *actor.Stopping:
        a.warnLog.Println("stopped actor")
        // cancela goroutines propias aquí
    case *MsgMio:
        _ = msg
    }
}
```

Reglas duras:

- `initLogs()` en `*actor.Started` **siempre**. Los loggers son `nil` hasta ese punto y
  cualquier `a.buildLog.Printf` antes revienta.
- Los mapas se crean en el constructor. Un actor que reinicia por supervisión **no** vuelve a
  pasar por el constructor si se usó `PropsFromProducer(func() actor.Actor { return a })` con
  una instancia ya creada (así se hace con `counting` y `listenner`): la instancia es la misma,
  el estado sobrevive al restart. Tenlo en cuenta al decidir qué es estado recuperable.
- Nunca bloquees dentro de `Receive`. El trabajo largo va a una goroutine lanzada en `Started`
  con un `context.CancelFunc` guardado en el struct y cancelado en `Stopping`
  (patrón de [client/listen-actor.go](../../../client/listen-actor.go)).
- Desde una goroutine solo se sale por `ctx.Send(...)` al propio actor o al padre; jamás
  mutes el estado del struct desde fuera del hilo del actor. Por eso los mapas van sin mutex.

## Ciclo de vida y supervisión

`errLog.Panicln(err)` **es el mecanismo de recuperación**, no un descuido: provoca que
protoactor reinicie el actor. Siempre precedido de `time.Sleep(3 * time.Second)` para evitar
un ciclo cerrado de reinicios:

```go
pid, err := ctx.SpawnNamed(props, "child")
if err != nil {
    time.Sleep(3 * time.Second)
    a.errLog.Panicln(err)
}
```

Cuando un fallo irrecuperable ocurre en una goroutine, se avisa al actor con un mensaje
centinela privado y el `Receive` es quien entra en panic (`msgListenError`, `msgPingError`).
Mantén ese estilo: mensaje centinela → panic en `Receive`.

## Jerarquía

`counting` es el hub: crea sus hijos (`events`, `doors`, `ping`, `gps`) dentro de su
`*actor.Started` y **enruta todo**. Los hijos no se conocen entre sí; se hablan por el padre
con `ctx.Send(ctx.Parent(), msg)` o `ctx.RequestFuture(ctx.Parent(), ...)`.

Un actor nuevo que necesite datos de otro hijo:

1. Lo spawnea `CountingActor` en `Started` y guarda el `*actor.PID` en un campo.
2. El hijo pide al padre; el padre reenvía con `ctx.RequestWithCustomSender(a.destino, msg, ctx.Sender())`
   para que la respuesta llegue al solicitante original (así funciona `MsgGetGps`).
3. Todo `RequestFuture` lleva timeout corto y camino de degradación. El GPS usa
   `180*time.Millisecond` y sigue con coordenada vacía si expira: **un evento de conteo nunca
   se pierde por falta de GPS**. Respeta esa prioridad.

## Persistencia (solo `CountingActor`)

Usa `persistence.Mixin` con provider boltdb (`pdb.NewBoltdbProvider`, snapshot cada 10 eventos,
ver [client/main/persistence.go](../../../client/main/persistence.go)).

- `a.PersistReceive(msg)` solo para `*messages.Event`, y **antes** de aplicar la lógica de
  negocio del evento.
- `if a.Recovering() { ...aplicar al estado y `break`... }`: durante replay se reconstruyen los
  contadores pero **no** se envía nada a `events` ni se publica en MQTT. Cualquier rama nueva
  en el manejo de eventos debe respetar esta guarda o duplicará publicaciones al reiniciar.
- `*persistence.RequestSnapshot` → copiar los mapas a `*messages.Snapshot` y `PersistSnapshot`.
  Si añades un mapa de estado, hay tres sitios que actualizar: el struct, el `.proto`
  (`Snapshot`), el bloque de `RequestSnapshot` y el de recuperación `case *messages.Snapshot`.
  Olvidar uno degrada la recuperación en silencio.
- Los campos escalares de `Snapshot` (`Inputs`, `Outputs`, ...) existen solo por
  retrocompatibilidad con bases de datos viejas. No los uses en código nuevo.

Cambiar el esquema de `Snapshot` obliga a considerar la migración de las bases ya desplegadas
en `/SD/boltdbs/countingdb`: mantén siempre lectura tolerante (`GetXMap() != nil`).

## Mensajes

- Cruzan persistencia o red → protobuf en `client/messages` + regenerar con
  `cd client/messages && ./protobuf.sh`.
- Internos al proceso → struct plano en el paquete `client`. Privado (`msgEvent`) si no sale
  del paquete; exportado (`MsgDoor`) si `main` u otro paquete lo construye.
