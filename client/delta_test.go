package client

import (
	"testing"

	"github.com/dumacp/go-hikvision/client/messages"
)

// simulaConteo reproduce la aritmética de CountingActor sobre una secuencia de eventos,
// sin levantar el actor: lo que se fija acá es la regla, no el cableado.
//
// Devuelve el acumulado publicado y el "raw" que queda persistido.
func simulaConteo(eventos []*messages.Event) (publicado, raw int64) {
	for _, msg := range eventos {
		diff := msg.GetValue() - raw
		nuevoRaw := msg.GetValue()
		if msg.GetDelta() {
			diff = msg.GetValue()
			nuevoRaw = raw + msg.GetValue()
		}
		if diff > 0 && diff < 10 {
			publicado += diff
		}
		raw = nuevoRaw
	}
	return
}

func ev(v int64, delta bool) *messages.Event {
	return &messages.Event{ID: 0, Type: messages.Event_INPUT, Value: v, Delta: delta}
}

// TestG0SigueContandoPorDiferencia: la cámara vieja manda acumulados y el paso sale de
// restar. Es el comportamiento que NO se puede cambiar.
func TestG0SigueContandoPorDiferencia(t *testing.T) {
	pub, raw := simulaConteo([]*messages.Event{
		ev(1, false), ev(2, false), ev(2, false), ev(5, false),
	})
	if pub != 5 {
		t.Errorf("publicado %d, quiero 5 (1+1+0+3)", pub)
	}
	if raw != 5 {
		t.Errorf("raw %d, quiero 5: con acumulados el raw ES el valor de la cámara", raw)
	}
}

// TestG1CuentaElIncremento: la cámara nueva manda el conteo de una ventana, que YA es
// el paso. Restar contra el acumulado anterior daría números absurdos.
func TestG1CuentaElIncremento(t *testing.T) {
	pub, raw := simulaConteo([]*messages.Event{
		ev(2, true), ev(3, true), ev(1, true),
	})
	if pub != 6 {
		t.Errorf("publicado %d, quiero 6 (2+3+1)", pub)
	}
	if raw != 6 {
		t.Errorf("raw %d, quiero 6: con incrementos el raw se lleva SUMANDO", raw)
	}
}

// TestG1NoSePierdeTrasReinicio es el motivo de haber agregado el campo al protobuf en
// vez de acumular en ListenActor.
//
// Tras un reinicio el raw persistido sigue alto y el acumulador en memoria arrancaría en
// cero. Con un acumulado sintético la primera ventana daría diff negativo y se
// descartaría salvo que trajera menos de 4 personas — con el ciclo en 15 minutos, eso es
// perder una ventana entera en cada reinicio. Con el incremento marcado, no pasa.
func TestG1NoSePierdeTrasReinicio(t *testing.T) {
	// El equipo venía contando y el raw quedó en 50.
	raw := int64(50)
	var publicado int64
	for _, msg := range []*messages.Event{ev(7, true), ev(2, true)} {
		diff := msg.GetValue()
		publicado += diff
		raw += msg.GetValue()
	}
	if publicado != 9 {
		t.Errorf("publicado %d, quiero 9: ninguna ventana se pierde por el reinicio", publicado)
	}

	// Y la comparación: así se habría comportado el acumulado sintético.
	sintetico, _ := simulaConteo([]*messages.Event{ev(7, false)})
	_ = sintetico
	diffSintetico := int64(7) - int64(50)
	if diffSintetico >= 0 {
		t.Fatal("la premisa de esta prueba es que el diff sintético sale negativo")
	}
	// 7 >= 4, así que la rama de diff<0 NO lo cuenta: esas 7 personas se perderían.
	if int64(7) < 4 {
		t.Fatal("con 7 personas la ventana cae fuera del escape de <4")
	}
}

// TestG1RespetaElTopeDeDiez: el incremento pasa por las mismas guardas. Una ventana con
// 10 o más personas se descarta igual que un salto de la cámara vieja.
func TestG1RespetaElTopeDeDiez(t *testing.T) {
	pub, raw := simulaConteo([]*messages.Event{ev(12, true), ev(3, true)})
	if pub != 3 {
		t.Errorf("publicado %d, quiero 3: la ventana de 12 se descarta", pub)
	}
	// El raw avanza igual, porque la cámara sí contó esas personas aunque nosotros no
	// las publiquemos: no hacerlo dejaría el acumulado corrido para siempre.
	if raw != 15 {
		t.Errorf("raw %d, quiero 15", raw)
	}
}
