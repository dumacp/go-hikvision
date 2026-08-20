package client

import (
	"testing"
	"time"
)

func at(hh, mm, ss int) time.Time {
	return time.Date(2026, 8, 20, hh, mm, ss, 0, time.Local)
}

func TestWithinWindow(t *testing.T) {
	casos := []struct {
		nombre   string
		t        time.Time
		ini, fin string
		quiero   bool
	}{
		// La ventana por defecto que se sugiere: dentro del hueco que deja
		// -recordStart=04:00:00 / -recordEnd=23:59:00.
		{"dentro de la franja normal", at(1, 0, 0), "00:30:00", "03:30:00", true},
		{"antes del inicio", at(0, 15, 0), "00:30:00", "03:30:00", false},
		{"justo en el inicio", at(0, 30, 0), "00:30:00", "03:30:00", true},
		// El final es exclusivo: si fuera inclusivo, una ventana que termina donde arranca
		// la grabación permitiría reiniciar en el primer segundo de servicio.
		{"justo en el fin", at(3, 30, 0), "00:30:00", "03:30:00", false},
		{"en plena jornada", at(14, 0, 0), "00:30:00", "03:30:00", false},

		// Cruce de medianoche: es el caso que una comparación ingenua ini<=x<fin invierte.
		{"cruce, antes de medianoche", at(23, 0, 0), "22:00:00", "04:00:00", true},
		{"cruce, después de medianoche", at(2, 0, 0), "22:00:00", "04:00:00", true},
		{"cruce, fuera", at(12, 0, 0), "22:00:00", "04:00:00", false},

		// Una ventana inválida no debe abrir la puerta: sin horas legibles no se reinicia.
		{"hora inválida", at(1, 0, 0), "esto no es una hora", "03:30:00", false},
		{"vacía", at(1, 0, 0), "", "", false},
	}
	for _, c := range casos {
		if got := withinWindow(c.t, c.ini, c.fin); got != c.quiero {
			t.Errorf("%s: withinWindow(%s, %q, %q) = %v, quiero %v",
				c.nombre, c.t.Format("15:04:05"), c.ini, c.fin, got, c.quiero)
		}
	}
}

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
	if vacia.rebootAllowed() {
		t.Error("una configuración vacía no debe permitir reinicios")
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

	// Media ventana no es ventana: con una sola de las dos horas no se reinicia.
	if (CameraConfig{RebootStart: "00:30:00"}).rebootAllowed() {
		t.Error("con solo -rebootStart no se debe reiniciar")
	}
	if !(CameraConfig{RebootStart: "00:30:00", RebootEnd: "03:30:00"}).rebootAllowed() {
		t.Error("con las dos horas se debe permitir el reinicio")
	}
}
