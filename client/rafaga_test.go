package client

import (
	"testing"
	"time"
)

// pasos arma una cola de una puerta con pasos en los segundos dados desde t0.
func pasos(t0 time.Time, puerta int32, segundos ...int) []videoJob {
	var out []videoJob
	for _, s := range segundos {
		out = append(out, videoJob{door: puerta, when: t0.Add(time.Duration(s) * time.Second)})
	}
	return out
}

var t0 = time.Date(2026, 11, 7, 10, 42, 50, 0, time.Local)

// La ráfaga real de FLO-W7-0138 del 2026-11-07: 6 pasos entre 10:42:50 y 10:43:17. Con la
// ventana fija salieron 5 clips; encadenada es uno solo de 43 s.
func TestRafagaRealEsUnSoloClip(t *testing.T) {
	cola := pasos(t0, 0, 0, 6, 12, 17, 23, 27)
	r := armarRafaga(cola, 10*time.Second, 16*time.Second, 10*time.Second, 60*time.Second)
	if len(r.indices) != 6 {
		t.Fatalf("los 6 pasos debían ir al mismo clip, fueron %d", len(r.indices))
	}
	if !r.desde.Equal(t0.Add(-10*time.Second)) || !r.hasta.Equal(t0.Add(33*time.Second)) {
		t.Errorf("ventana %s..%s, se esperaba 10:42:40..10:43:23",
			r.desde.Format("15:04:05"), r.hasta.Format("15:04:05"))
	}
	if got := r.hasta.Sub(r.desde); got != 43*time.Second {
		t.Errorf("el clip debía durar 43 s, dura %v", got)
	}
}

// Sin encadenar se comporta exactamente como antes: la misma cola da un clip por paso,
// salvo los dos últimos, que caen a 4 s.
func TestSinEncadenarEsLaVentanaFijaDeSiempre(t *testing.T) {
	cola := pasos(t0, 0, 0, 6, 12, 17, 23, 27)
	clips := 0
	for len(cola) > 0 {
		r := armarRafaga(cola, 10*time.Second, 16*time.Second, 0, 60*time.Second)
		if d := r.hasta.Sub(r.desde); d != 16*time.Second {
			t.Errorf("sin encadenar la ventana es fija de 16 s, fue %v", d)
		}
		resto := cola[:0:0]
		en := map[int]bool{}
		for _, i := range r.indices {
			en[i] = true
		}
		for i, j := range cola {
			if !en[i] {
				resto = append(resto, j)
			}
		}
		cola = resto
		clips++
	}
	if clips != 5 {
		t.Errorf("con la ventana fija esa ráfaga son 5 clips (lo medido), fueron %d", clips)
	}
}

func TestUnPasoSoloEsElClipDeSiempre(t *testing.T) {
	r := armarRafaga(pasos(t0, 0, 0), 10*time.Second, 16*time.Second, 10*time.Second, 60*time.Second)
	if d := r.hasta.Sub(r.desde); d != 16*time.Second || !r.desde.Equal(t0.Add(-10*time.Second)) {
		t.Errorf("un paso solo debía dar 10 s antes y 6 después: %s..%s", r.desde, r.hasta)
	}
}

func TestLaRafagaSeCortaConUnHuecoMayorAlGap(t *testing.T) {
	r := armarRafaga(pasos(t0, 0, 0, 8, 25), 10*time.Second, 16*time.Second, 10*time.Second, 60*time.Second)
	if len(r.indices) != 2 {
		t.Errorf("el paso a 17 s del anterior abre otro clip: grupo de %d", len(r.indices))
	}
}

// Una parada con gente subiendo sin parar: el tope cierra el clip y lo que sigue queda en
// la cola para el siguiente.
func TestElTopeCortaUnaRafagaQueNoTermina(t *testing.T) {
	var seg []int
	for s := 0; s <= 120; s += 5 {
		seg = append(seg, s)
	}
	cola := pasos(t0, 0, seg...)
	r := armarRafaga(cola, 10*time.Second, 16*time.Second, 10*time.Second, 60*time.Second)
	if d := r.hasta.Sub(r.desde); d > 60*time.Second {
		t.Errorf("el clip se pasó del tope: %v", d)
	}
	if len(r.indices) == len(cola) {
		t.Error("con el tope, no todos los pasos podían entrar al primer clip")
	}
}

func TestOtraPuertaNoEntraAlClip(t *testing.T) {
	cola := append(pasos(t0, 0, 0), pasos(t0, 1, 3)...)
	cola = append(cola, pasos(t0, 0, 7)...)
	r := armarRafaga(cola, 10*time.Second, 16*time.Second, 10*time.Second, 60*time.Second)
	if len(r.indices) != 2 || r.indices[1] != 2 {
		t.Errorf("solo los pasos de la puerta 0: %v", r.indices)
	}
}

// Un tope menor que la ventana de un paso no puede achicar el clip de siempre.
func TestTopeMenorQueLaVentanaNoAchica(t *testing.T) {
	r := armarRafaga(pasos(t0, 0, 0, 6), 10*time.Second, 16*time.Second, 10*time.Second, 5*time.Second)
	if d := r.hasta.Sub(r.desde); d != 16*time.Second {
		t.Errorf("la ventana de un paso no puede achicarse: %v", d)
	}
}
