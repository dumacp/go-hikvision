package client

import (
	"testing"

	"github.com/asynkron/protoactor-go/actor"
)

// Pruebas de la parte de la aplicación que no necesita un sistema de actores. El respawn de
// los hijos sí lo necesita y se verifica en el equipo; lo que se fija acá son las decisiones
// que se toman ANTES de tocar el árbol, que son las que pueden dejar al binario sin contar.

func contadorConConfig(cfg Efectiva) *CountingActor {
	a := NewCountingActor()
	a.initLogs()
	a.SetConfig(cfg, nil)
	return a
}

// TestSetConfigAplicaElGatingDeCadaPuerta. openState y countCloseDoor son lo que decide si
// un cruce se cuenta o se descarta; si SetConfig no los vuelca, el binario arranca con el
// mapa vacío y el default de openState (1) invierte el gating de un vehículo cableado al
// revés, descartando todos los cruces sin un solo error.
func TestSetConfigAplicaElGatingDeCadaPuerta(t *testing.T) {
	cfg := Efectiva{
		ZeroOpenState:      map[int]bool{0: true, 1: false},
		CountWithCloseDoor: map[int]bool{0: true, 1: false},
	}
	a := contadorConConfig(cfg)

	// zeroOpenState=true significa que el estado "abierta" llega como 0.
	if a.openState[0] != 0 {
		t.Errorf("puerta 0: openState = %d, quiero 0", a.openState[0])
	}
	if a.openState[1] != 1 {
		t.Errorf("puerta 1: openState = %d, quiero 1", a.openState[1])
	}
	if !a.countCloseDoor[0] || a.countCloseDoor[1] {
		t.Errorf("countCloseDoor = %v", a.countCloseDoor)
	}
}

// TestSetConfigFijaLaCamaraDelKeepAlive. Sin esto el keep-alive consulta la dirección
// histórica aunque haya cámaras configuradas, y publica un CounterDisconnected cada 300 s
// mientras las cámaras cuentan sin problema. Es el falso positivo que ya se arregló una vez.
func TestSetConfigFijaLaCamaraDelKeepAlive(t *testing.T) {
	a := contadorConConfig(Efectiva{
		Camaras:            []string{"", "192.168.186.91"},
		ZeroOpenState:      map[int]bool{},
		CountWithCloseDoor: map[int]bool{},
	})
	if a.pingAddr != "192.168.186.91" {
		t.Errorf("pingAddr = %q, quiero la primera cámara configurada", a.pingAddr)
	}
}

// TestVideoApagadoNoCreaElActor: es el interruptor que la plataforma maneja. Con
// videoEnabled en false no tiene que existir el hijo, aunque la fábrica pueda construirlo.
func TestVideoApagadoNoCreaElActor(t *testing.T) {
	llamadas := 0
	fabrica := func(Efectiva) *actor.Props {
		llamadas++
		return actor.PropsFromProducer(func() actor.Actor { return NewGPSActor() })
	}

	a := contadorConConfig(Efectiva{
		VideoEnabled:       false,
		VideoDir:           "/SD/video265/",
		ZeroOpenState:      map[int]bool{},
		CountWithCloseDoor: map[int]bool{},
	})
	a.SetVideoFactory(fabrica)

	if props := a.propsVideo(); props != nil {
		t.Error("con videoEnabled=false no debe crearse el actor de video")
	}
	if llamadas != 0 {
		t.Errorf("la fábrica no debería ni consultarse, se llamó %d veces", llamadas)
	}

	// Y prendiéndolo sí.
	a.cfg.VideoEnabled = true
	if props := a.propsVideo(); props == nil {
		t.Error("con videoEnabled=true debe crearse el actor de video")
	}
}

// TestSinFabricaNoHayHijo: un binario sin extracción ni mantenimiento de cámara se comporta
// como antes de que existiera nada de esto.
func TestSinFabricaNoHayHijo(t *testing.T) {
	a := contadorConConfig(Efectiva{
		VideoEnabled:       true,
		ZeroOpenState:      map[int]bool{},
		CountWithCloseDoor: map[int]bool{},
	})
	if a.propsVideo() != nil || a.propsCamera() != nil {
		t.Error("sin fábrica no puede haber hijo")
	}
}

// TestPropsPingSigueALaConfiguracion: al cambiar la cámara de una puerta, el keep-alive
// tiene que seguirla. Si no, queda consultando una dirección que ya no es de nadie y vuelve
// el CounterDisconnected falso.
func TestPropsPingSigueALaConfiguracion(t *testing.T) {
	a := contadorConConfig(Efectiva{
		Camaras:            []string{"10.0.0.1"},
		ZeroOpenState:      map[int]bool{},
		CountWithCloseDoor: map[int]bool{},
	})
	a.cfg.Camaras = []string{"10.0.0.9"}

	if got := a.nuevoPing().destino(); got != "10.0.0.9" {
		t.Errorf("el keep-alive consulta %q, quiero 10.0.0.9", got)
	}
	if a.propsPing() == nil {
		t.Error("el ping siempre existe")
	}
}
