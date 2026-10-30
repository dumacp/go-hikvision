package client

import "testing"

// TestPingDestino fija las dos formas, y sobre todo que la de siempre no cambie.
//
// Un equipo SIN -camera tiene que seguir consultando 192.168.188.21: es lo que hacen
// los que ya están desplegados y no hay razón para moverlos. Con -camera se consulta
// esa cámara, que es lo que evita el CounterDisconnected falso cada 300 s en un
// gateway con otro direccionamiento.
func TestPingDestino(t *testing.T) {
	if got := (&PingActor{}).destino(); got != pingIP {
		t.Errorf("sin addr debe quedar la dirección histórica, got %q", got)
	}
	if got := (&PingActor{addr: "192.168.186.93"}).destino(); got != "192.168.186.93" {
		t.Errorf("con addr debe usarla, got %q", got)
	}
}
