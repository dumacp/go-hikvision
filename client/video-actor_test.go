package client

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

// nuevoVideoActorTest arma un actor con los logs capturados en buffers, para poder
// afirmar sobre lo que se registra y —sobre todo— sobre lo que NO se repite.
func nuevoVideoActorTest(minFree uint64) (*VideoActor, *bytes.Buffer, *bytes.Buffer) {
	var warn, info bytes.Buffer
	a := &VideoActor{dir: "/SD/video", minFree: minFree}
	a.Logger = &Logger{}
	a.SetLogWarn(log.New(&warn, "", 0)).
		SetLogInfo(log.New(&info, "", 0)).
		SetLogError(log.New(&bytes.Buffer{}, "", 0)).
		SetLogBuild(log.New(&bytes.Buffer{}, "", 0))
	return a, &warn, &info
}

func lineas(b *bytes.Buffer) int {
	s := strings.TrimSpace(b.String())
	if len(s) == 0 {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// TestEspacioAvisaUnaSolaVez es el motivo de todo este camino: con el disco al tope
// fallan TODAS las extracciones, así que un aviso por evento serían cientos de líneas
// diarias idénticas, y el aviso quedaría enterrado bajo sus propias repeticiones.
func TestEspacioAvisaUnaSolaVez(t *testing.T) {
	a, warn, info := nuevoVideoActorTest(512 << 20)
	sinEspacio := &msgVideoDone{sinEspacio: true, espacioMedido: true, libre: 100 << 20}

	for i := 0; i < 50; i++ {
		a.reportarEspacio(sinEspacio)
	}
	if got := lineas(warn); got != 1 {
		t.Fatalf("50 eventos sin espacio dejaron %d avisos, quiero exactamente 1", got)
	}
	if !strings.Contains(warn.String(), "100 MB") || !strings.Contains(warn.String(), "512 MB") {
		t.Errorf("el aviso debe decir cuánto hay y cuánto se pide: %q", warn.String())
	}
	// No se borra nada, y el aviso tiene que decirlo: es la pregunta que se hace quien lo lee.
	if !strings.Contains(warn.String(), "no se borra") &&
		!strings.Contains(warn.String(), "No se borra") {
		t.Errorf("el aviso debe aclarar que no se borra ningún clip: %q", warn.String())
	}
	if lineas(info) != 0 {
		t.Errorf("no debería haber INFO todavía: %q", info.String())
	}

	// Al recuperarse: un INFO, y el aviso se rearma para que un segundo episodio se vea.
	a.reportarEspacio(&msgVideoDone{espacioMedido: true, libre: 900 << 20})
	if got := lineas(info); got != 1 {
		t.Fatalf("la recuperación dejó %d líneas INFO, quiero 1", got)
	}
	a.reportarEspacio(sinEspacio)
	if got := lineas(warn); got != 2 {
		t.Errorf("un segundo episodio debe volver a avisar: %d avisos, quiero 2", got)
	}
}

// TestEspacioRecuperadoSinAvisoPrevioNoHablaDeMas: si nunca faltó espacio, una extracción
// normal no tiene por qué dejar rastro.
func TestEspacioRecuperadoSinAvisoPrevioNoHablaDeMas(t *testing.T) {
	a, warn, info := nuevoVideoActorTest(512 << 20)
	for i := 0; i < 10; i++ {
		a.reportarEspacio(&msgVideoDone{espacioMedido: true, libre: 900 << 20})
	}
	if lineas(warn) != 0 || lineas(info) != 0 {
		t.Errorf("con espacio de sobra no debe registrarse nada: warn=%q info=%q",
			warn.String(), info.String())
	}
}

// TestNoPoderMedirAvisaUnaVez fija el fail-open: no poder medir el disco NO detiene la
// extracción —dejar al vehículo sin video porque falló un Statfs es peor que el riesgo
// que evita— pero tampoco puede pasar en silencio, porque entonces no hay forma de
// auditar por qué el piso no se estaba vigilando.
func TestNoPoderMedirAvisaUnaVez(t *testing.T) {
	a, warn, _ := nuevoVideoActorTest(512 << 20)
	fallo := &msgVideoDone{medirErr: errors.New("statfs: permission denied")}

	for i := 0; i < 20; i++ {
		a.reportarEspacio(fallo)
	}
	if got := lineas(warn); got != 1 {
		t.Fatalf("20 fallos de medición dejaron %d avisos, quiero 1", got)
	}
	if !strings.Contains(warn.String(), "sigue extrayendo") {
		t.Errorf("el aviso debe decir que la extracción continúa: %q", warn.String())
	}

	// Una medición buena rearma el aviso: el problema puede volver y hay que verlo.
	a.reportarEspacio(&msgVideoDone{espacioMedido: true, libre: 900 << 20})
	a.reportarEspacio(fallo)
	if got := lineas(warn); got != 2 {
		t.Errorf("tras una medición buena debe poder volver a avisar: %d, quiero 2", got)
	}
}

// TestMinFreeCeroDeshabilita comprueba el escape: con el piso en 0 no hay comprobación.
// El caso vive en la goroutine de extracción, así que acá se fija solo lo que se muestra.
func TestDescribeMinFree(t *testing.T) {
	if got := describeMinFree(0); got != "sin piso de disco" {
		t.Errorf("describeMinFree(0) = %q", got)
	}
	if got := describeMinFree(VideoMinFreeDefault); got != "512 MB" {
		t.Errorf("describeMinFree(default) = %q, quiero 512 MB", got)
	}
}

// TestVideoMinFreeDefault fija el default histórico: cambiarlo altera el comportamiento
// de todos los equipos desplegados que no pasan el flag.
func TestVideoMinFreeDefault(t *testing.T) {
	if VideoMinFreeDefault != 512<<20 {
		t.Errorf("el piso por defecto es %d, debe seguir siendo 512 MiB", VideoMinFreeDefault)
	}
}
