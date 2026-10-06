package isapi

import "testing"

// trackReal es el XML recortado tal como lo devuelve el DS-2XM6825G0 de laboratorio
// (192.168.186.91), para que la prueba hable del documento de verdad y no de uno inventado.
const trackReal = `<?xml version="1.0" encoding="UTF-8"?>
<Track version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
<id>101</id><Channel>101</Channel><Enable>true</Enable>
<Description>trackType=standard,codecType=H.265</Description>
<TrackGUID>e32e6863-ea5e-4ee4-997e-bc9b5ee7ef05</TrackGUID>
<DefaultRecordingMode>CMR</DefaultRecordingMode>
<LoopEnable>true</LoopEnable>
<SrcDescriptor><SrcChannel>1</SrcChannel></SrcDescriptor>
</Track>`

func TestLoopEnabledFrom(t *testing.T) {
	loop, vino := LoopEnabledFrom([]byte(trackReal))
	if !vino {
		t.Fatal("el campo LoopEnable está en el XML real y debe leerse")
	}
	if !loop {
		t.Error("la cámara de laboratorio tiene la sobrescritura ENCENDIDA")
	}

	apagada := []byte(`<Track><id>101</id><LoopEnable>false</LoopEnable></Track>`)
	if loop, vino := LoopEnabledFrom(apagada); !vino || loop {
		t.Errorf("con false debe leerse false: loop=%v vino=%v", loop, vino)
	}

	// Un modelo que no reporte el campo NO debe disparar la alarma por omisión: ausente
	// no es lo mismo que apagado.
	if _, vino := LoopEnabledFrom([]byte(`<Track><id>101</id></Track>`)); vino {
		t.Error("sin el campo, vino debe ser false para que nadie alarme por omisión")
	}
	if _, vino := LoopEnabledFrom([]byte("no es xml")); vino {
		t.Error("con XML inválido tampoco debe alarmar")
	}
}

// TestPrimeroEligeElMedioRoto: con varios medios hay que reportar el que falla, no el
// primero de la lista. Antes solo se miraba HDDs[0] y una SD rota en segunda posición
// quedaba invisible.
func TestPrimeroEligeElMedioRoto(t *testing.T) {
	s := &StorageStatus{HDDs: []HDD{
		{ID: 1, Status: "ok", Capacity: 7695, FreeSpace: 1536},
		{ID: 2, Status: "unformatted", Capacity: 7695, FreeSpace: 0},
	}}
	h, ok := s.Primero()
	if !ok || h.ID != 2 {
		t.Errorf("debe elegir el medio roto: id=%d ok=%v", h.ID, ok)
	}

	// Con todos sanos, el primero.
	s = &StorageStatus{HDDs: []HDD{
		{ID: 1, Status: "ok", FreeSpace: 1536},
		{ID: 2, Status: "ok", FreeSpace: 10},
	}}
	if h, ok := s.Primero(); !ok || h.ID != 1 {
		t.Errorf("con todos sanos debe elegir el primero: id=%d", h.ID)
	}

	// Sin medios no hay nada que reportar, y eso no es lo mismo que un medio roto.
	if _, ok := (&StorageStatus{}).Primero(); ok {
		t.Error("sin medios debe devolver ok=false")
	}
}

// TestHealthy fija qué cuenta como sano, que es lo que dispara el evento.
func TestHealthy(t *testing.T) {
	for _, c := range []struct {
		status string
		sano   bool
	}{
		{"ok", true}, {"OK", true}, {"Ok", true},
		{"unformatted", false}, {"error", false}, {"idle", false}, {"", false},
		{StatusNoOverwrite, false},
	} {
		if got := (HDD{Status: c.status}).Healthy(); got != c.sano {
			t.Errorf("Healthy(%q) = %v, quiero %v", c.status, got, c.sano)
		}
	}
}
