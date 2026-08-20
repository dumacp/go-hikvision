package client

import "testing"

func TestSmartCodecWanted(t *testing.T) {
	casos := []struct {
		in     string
		valor  bool
		pedido bool
	}{
		{"off", false, true},
		{"OFF", false, true},
		{" off ", false, true},
		{"false", false, true},
		{"0", false, true},
		{"on", true, true},
		{"true", true, true},
		// Lo importante: vacío significa "no toques nada", no "apagalo". Si devolviera
		// (false, true) el actor escribiría el encoder de toda la flota sin que nadie lo
		// pidiera, y eso termina en un reinicio.
		{"", false, false},
		{"quizás", false, false},
	}
	for _, c := range casos {
		valor, pedido := smartCodecWanted(c.in)
		if valor != c.valor || pedido != c.pedido {
			t.Errorf("smartCodecWanted(%q) = (%v, %v), quiero (%v, %v)",
				c.in, valor, pedido, c.valor, c.pedido)
		}
	}
}

// TestCameraConfigGates verifica los dos interruptores que mantienen el comportamiento
// anterior cuando no se pide nada: sin flags no se toca el encoder y no se reinicia.
func TestCameraConfigGates(t *testing.T) {
	var vacia CameraConfig
	if vacia.wantsEncoder() {
		t.Error("una configuración vacía no debe tocar el encoder")
	}

	if !(CameraConfig{SmartCodec: "off"}).wantsEncoder() {
		t.Error("-smartCodec off debe habilitar la revisión del encoder")
	}
	if !(CameraConfig{FrameRate: 20}).wantsEncoder() {
		t.Error("-videoFrameRate debe habilitar la revisión del encoder")
	}
	if !(CameraConfig{GopFrames: 20}).wantsEncoder() {
		t.Error("-videoGop debe habilitar la revisión del encoder")
	}
}
