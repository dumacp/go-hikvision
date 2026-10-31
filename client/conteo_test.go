package client

import (
	"testing"

	"github.com/dumacp/go-hikvision/client/messages"
)

// Estas pruebas fijan la aritmética de CountingActor, que es la ÚNICA regla de conteo:
// las dos generaciones de cámara mandan ACUMULADOS y el paso sale de restar contra el
// último valor guardado.
//
// Hubo un segundo camino —el conteo por ventana de la DS-2XM6825G1 con firmware
// V3.2.2, marcado con un campo `Delta` en el protobuf— y se quitó entero: ese formato
// no dice en qué instante cruzó cada persona, así que nunca podía tener video, y
// mantener vivo un camino que no se puede probar solo confundía el diagnóstico.
//
// simulaConteo reproduce la aritmética sobre una secuencia de eventos, sin levantar el
// actor: lo que se fija acá es la regla, no el cableado. Devuelve el acumulado
// publicado y el "raw" que queda persistido.
func simulaConteo(eventos []*messages.Event) (publicado, raw int64) {
	for _, msg := range eventos {
		diff := msg.GetValue() - raw
		switch {
		case diff > 0 && diff < 10:
			publicado += diff
		case diff < 0 && msg.GetValue() < 4:
			// La cámara reinició su contador: se acepta el valor como paso, pero solo
			// si es pequeño. Un salto negativo grande es corrupción, no un reinicio.
			publicado += msg.GetValue()
		}
		// El raw sigue SIEMPRE al acumulado de la cámara, incluso cuando el paso se
		// descarta: no hacerlo dejaría la referencia corrida para siempre y el evento
		// siguiente volvería a descartarse.
		raw = msg.GetValue()
	}
	return
}

func ev(v int64) *messages.Event {
	return &messages.Event{ID: 0, Type: messages.Event_INPUT, Value: v}
}

// TestConteoPorDiferencia: el caso normal. La cámara manda acumulados y el paso sale de
// restar; un valor repetido no cuenta nada, que es lo que pasa en cada evento con el
// contador del sentido contrario.
func TestConteoPorDiferencia(t *testing.T) {
	pub, raw := simulaConteo([]*messages.Event{ev(1), ev(2), ev(2), ev(5)})
	if pub != 5 {
		t.Errorf("publicado %d, quiero 5 (1+1+0+3)", pub)
	}
	if raw != 5 {
		t.Errorf("raw %d, quiero 5: el raw ES el acumulado de la cámara", raw)
	}
}

// TestSaltoGrandeSeDescarta: diez o más de golpe no es tráfico real, es el acumulado
// desincronizado. Se descarta el paso pero el raw se reancla, para no quedar descartando
// para siempre.
func TestSaltoGrandeSeDescarta(t *testing.T) {
	pub, raw := simulaConteo([]*messages.Event{ev(2), ev(14), ev(16)})
	if pub != 4 {
		t.Errorf("publicado %d, quiero 4: cuentan el 2 inicial y el 2 final; el salto "+
			"de 12 que hay en medio se descarta", pub)
	}
	if raw != 16 {
		t.Errorf("raw %d, quiero 16: el raw se reancla aunque el paso se descarte", raw)
	}
}

// TestReinicioDelContadorDeLaCamara: la cámara vuelve a empezar de cero —reinicio,
// cambio de día— y el diff sale negativo. Solo se acepta si el valor nuevo es menor que
// 4; más que eso es corrupción y no se publica.
func TestReinicioDelContadorDeLaCamara(t *testing.T) {
	pub, raw := simulaConteo([]*messages.Event{ev(50), ev(1)})
	if pub != 1 {
		t.Errorf("publicado %d, quiero 1: tras el reinicio el 1 se acepta", pub)
	}
	if raw != 1 {
		t.Errorf("raw %d, quiero 1", raw)
	}

	pub2, _ := simulaConteo([]*messages.Event{ev(50), ev(7)})
	if pub2 != 0 {
		t.Errorf("publicado %d, quiero 0: un 7 tras un 50 no es un reinicio creíble", pub2)
	}
}
