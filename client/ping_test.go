package client

import "testing"

// TestPingDestino fija las dos formas, y sobre todo que la de siempre no cambie.
//
// Un equipo SIN -camera tiene que seguir consultando 192.168.188.21, que es la
// dirección ESTÁNDAR de la cámara en el despliegue normal. No es un valor heredado ni
// mal elegido: es el que tienen los equipos en campo. Con -camera se consulta esa
// cámara, que es lo que evita el CounterDisconnected falso cada 300 s en un gateway
// con otro direccionamiento, como un banco de pruebas.
func TestPingDestino(t *testing.T) {
	if got := (&PingActor{}).destino(); got != pingIP {
		t.Errorf("sin addr debe quedar la dirección estándar, got %q", got)
	}
	if got := (&PingActor{addr: "192.168.186.93"}).destino(); got != "192.168.186.93" {
		t.Errorf("con addr debe usarla, got %q", got)
	}
}
