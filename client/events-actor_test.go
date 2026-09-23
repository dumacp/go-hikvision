package client

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/dumacp/go-hikvision/client/messages"
)

func loggerPrueba() *Logger {
	l := &Logger{}
	nulo := log.New(os.NewFile(0, os.DevNull), "", 0)
	l.SetLogError(nulo).SetLogWarn(nulo).SetLogInfo(nulo).SetLogBuild(nulo)
	return l
}

func eventoPrueba() *messages.Event {
	return &messages.Event{
		Type:  messages.Event_INPUT,
		ID:    0,
		Value: 1,
		Uid:   "ce9f5d9d",
	}
}

// TestCountersDoorLlevaEventID: el comportamiento por defecto no cambia. Es la mitad
// que impide que la compuerta de compatibilidad se vuelva el camino normal sin que
// nadie lo note.
func TestCountersDoorLlevaEventID(t *testing.T) {
	raw := buildEventPass(nil, eventoPrueba(), "$GPRMC,x", map[uint]uint{}, loggerPrueba(), false)
	if !strings.Contains(string(raw), `"event_id":"ce9f5d9d"`) {
		t.Fatalf("por defecto el COUNTERSDOOR debe llevar event_id: %s", raw)
	}
}

// TestSinEventIDVuelveAlFormatoPrevio es el contrato con la plataforma: con el flag,
// la clave event_id NO aparece. No basta con que vaya vacía —un "event_id":"" sigue
// siendo un campo desconocido para quien no lo espera— tiene que no estar.
func TestSinEventIDVuelveAlFormatoPrevio(t *testing.T) {
	raw := buildEventPass(nil, eventoPrueba(), "$GPRMC,x", map[uint]uint{}, loggerPrueba(), true)
	if strings.Contains(string(raw), "event_id") {
		t.Fatalf("con -withoutEventID la clave no debe aparecer en absoluto: %s", raw)
	}

	// Y el resto del mensaje tiene que quedar intacto: el flag omite un campo, no
	// reescribe el evento.
	var m struct {
		Type  string `json:"type"`
		Value struct {
			Coord    string  `json:"coord"`
			ID       int     `json:"id"`
			State    uint    `json:"state"`
			Counters []int64 `json:"counters"`
			Type     string  `json:"type"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("el JSON quedó inválido: %s (%s)", err, raw)
	}
	if m.Type != "COUNTERSDOOR" {
		t.Errorf("type = %q, quiero COUNTERSDOOR", m.Type)
	}
	if m.Value.Type != "CAMERA" {
		t.Errorf("value.type = %q, quiero CAMERA", m.Value.Type)
	}
	if m.Value.Coord != "$GPRMC,x" {
		t.Errorf("value.coord = %q", m.Value.Coord)
	}
	if len(m.Value.Counters) != 2 || m.Value.Counters[0] != 1 || m.Value.Counters[1] != 0 {
		t.Errorf("value.counters = %v, quiero [1 0] para una entrada", m.Value.Counters)
	}
}

// TestSinEventIDSoloQuitaEseCampo compara las dos formas: la única diferencia entre
// el JSON con flag y sin flag debe ser event_id. Si alguien agrega otro campo al
// COUNTERSDOOR sin pensar en la compatibilidad, esta prueba lo dice.
func TestSinEventIDSoloQuitaEseCampo(t *testing.T) {
	claves := func(sin bool) map[string]bool {
		raw := buildEventPass(nil, eventoPrueba(), "$GPRMC,x", map[uint]uint{}, loggerPrueba(), sin)
		var m struct {
			Value map[string]interface{} `json:"value"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		k := make(map[string]bool, len(m.Value))
		for n := range m.Value {
			k[n] = true
		}
		return k
	}
	con, sin := claves(false), claves(true)
	for n := range con {
		if !sin[n] && n != "event_id" {
			t.Errorf("con -withoutEventID se perdió el campo %q, que no es event_id", n)
		}
	}
	for n := range sin {
		if !con[n] {
			t.Errorf("con -withoutEventID apareció un campo nuevo: %q", n)
		}
	}
	if !con["event_id"] {
		t.Error("el JSON por defecto debería tener event_id")
	}
	if sin["event_id"] {
		t.Error("el JSON con el flag no debería tener event_id")
	}
}
