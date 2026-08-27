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

func TestNormalizeVideoCodec(t *testing.T) {
	ok := map[string]string{
		"":       "",
		"h264":   "H.264",
		"H.264":  "H.264",
		"264":    "H.264",
		"avc":    "H.264",
		"h265":   "H.265",
		"H.265":  "H.265",
		" H265 ": "H.265",
		"hevc":   "H.265",
	}
	for in, quiero := range ok {
		got, err := NormalizeVideoCodec(in)
		if err != nil {
			t.Errorf("NormalizeVideoCodec(%q) devolvió error: %s", in, err)
			continue
		}
		if got != quiero {
			t.Errorf("NormalizeVideoCodec(%q) = %q, quiero %q", in, got, quiero)
		}
	}
	// Un typo tiene que detener el arranque, no quedar en "no pedido": si se ignora, la
	// cámara sigue con el codec viejo mientras la configuración dice otra cosa.
	for _, in := range []string{"h26", "h.2645", "vp9", "265h", "mjpeg"} {
		if _, err := NormalizeVideoCodec(in); err == nil {
			t.Errorf("NormalizeVideoCodec(%q) no devolvió error", in)
		}
	}
}

func TestNormalizeSmartCodec(t *testing.T) {
	for _, in := range []string{"", "on", "off", "OFF", " on ", "true", "false", "0", "1"} {
		if _, err := NormalizeSmartCodec(in); err != nil {
			t.Errorf("NormalizeSmartCodec(%q) devolvió error: %s", in, err)
		}
	}
	for _, in := range []string{"tru", "apagado", "sí", "enable"} {
		if _, err := NormalizeSmartCodec(in); err == nil {
			t.Errorf("NormalizeSmartCodec(%q) no devolvió error", in)
		}
	}
}

// TestCameraConfigCodecGate verifica que pedir solo el codec ya habilite la revisión del
// encoder, sin necesidad de pasar además smartCodec o fps.
func TestCameraConfigCodecGate(t *testing.T) {
	if !(CameraConfig{VideoCodec: "H.265"}).wantsEncoder() {
		t.Error("-videoCodec debe habilitar la revisión del encoder")
	}
}
