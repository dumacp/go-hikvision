package client

import (
	"strings"
	"testing"
	"time"
)

// Estos son los payloads REALES capturados del log de FLO-W7-0138 el 2026-10-29, de
// /usr/sbin/omvz7/ignition. Están acá por la misma razón que el XML de la cámara en
// peoplecounting/g1_test.go: el contrato con go-ignition es JSON más un string mágico,
// y la única forma de que un cambio de su lado no nos sorprenda en el vehículo es tener
// su salida de verdad congelada en una prueba.
const (
	// El aviso con periodo de gracia, que es sobre el que actuamos.
	jsonSuspendUP = `{"value":{"state":1},"timestamp":1793335980.405281,"type":"SUSPEND_EVT"}`
	// El estado crudo de la llave, que solo se registra.
	jsonIgnitionUP = `{"value":{"state":1},"timestamp":1793335980.4018128,"type":"IgnitionEvent"}`
)

// TestParseIgnitionLosDosFlancos: lo que importa es que el apagado y el encendido
// lleguen los dos, porque de nada sirve cortar la energía si no sabemos devolverla.
func TestParseIgnitionLosDosFlancos(t *testing.T) {
	casos := []struct {
		nombre    string
		json      string
		tipo      string
		encendido bool
	}{
		{"suspend UP", jsonSuspendUP, tipoSuspendEvt, true},
		{"suspend DOWN", strings.Replace(jsonSuspendUP, `"state":1`, `"state":2`, 1), tipoSuspendEvt, false},
		{"ignition UP", jsonIgnitionUP, tipoIgnitionEvt, true},
		{"ignition DOWN", strings.Replace(jsonIgnitionUP, `"state":1`, `"state":2`, 1), tipoIgnitionEvt, false},
		{"con coordenada del GPS", `{"value":{"state":2,"coord":"$GPRMC,1,2,3"},"timestamp":1793335980.4,"type":"SUSPEND_EVT"}`, tipoSuspendEvt, false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			res, err := parseIgnitionEvents([]byte(c.json))
			if err != nil {
				t.Fatalf("no debería fallar: %s", err)
			}
			ev, ok := res.(*MsgIgnition)
			if !ok {
				t.Fatalf("tipo %T, quiero *MsgIgnition", res)
			}
			if ev.Tipo != c.tipo {
				t.Errorf("Tipo %q, quiero %q", ev.Tipo, c.tipo)
			}
			if ev.Encendido != c.encendido {
				t.Errorf("Encendido %v, quiero %v", ev.Encendido, c.encendido)
			}
		})
	}
}

// TestParseIgnitionTimestampEnSegundos fija la unidad. go-ignition manda SEGUNDOS con
// fracción en el JSON y MILISEGUNDOS enteros en el PowerEvent que viaja por protoactor.
// Leerlo con la unidad equivocada da una fecha del año 58 mil o de 1970, y en los dos
// casos el log del apagado apuntaría a cualquier lado.
func TestParseIgnitionTimestampEnSegundos(t *testing.T) {
	res, err := parseIgnitionEvents([]byte(jsonSuspendUP))
	if err != nil {
		t.Fatal(err)
	}
	ev := res.(*MsgIgnition)
	if got := ev.Timestamp.Unix(); got != 1793335980 {
		t.Errorf("Timestamp.Unix() = %d, quiero 1793335980", got)
	}
	// La fracción no se pierde del todo: 0.405281 s son ~405 ms.
	if ms := ev.Timestamp.Nanosecond() / 1e6; ms < 404 || ms > 406 {
		t.Errorf("la fracción quedó en %d ms, quiero ~405", ms)
	}
}

// TestParseIgnitionTimestampCeroUsaElRelojLocal: go-ignition omite el campo si falla al
// armarlo. Una hora de 1970 en el log sería peor que una aproximada.
func TestParseIgnitionTimestampCeroUsaElRelojLocal(t *testing.T) {
	res, err := parseIgnitionEvents([]byte(`{"value":{"state":2},"type":"SUSPEND_EVT"}`))
	if err != nil {
		t.Fatal(err)
	}
	ev := res.(*MsgIgnition)
	if time.Since(ev.Timestamp) > time.Minute {
		t.Errorf("sin timestamp debería usar el reloj local, quedó en %s", ev.Timestamp)
	}
}

// TestParseIgnitionRechaza: lo que NO se puede interpretar no se interpreta. En
// particular el estado NA, que es "no sé": tratarlo como apagado dejaría la cámara sin
// energía por un dato que el propio go-ignition declara desconocido.
func TestParseIgnitionRechaza(t *testing.T) {
	casos := []struct {
		nombre string
		json   string
		dice   string
	}{
		{"estado NA", `{"value":{"state":0},"timestamp":1,"type":"SUSPEND_EVT"}`, "NA"},
		{"estado desconocido", `{"value":{"state":9},"timestamp":1,"type":"SUSPEND_EVT"}`, "9"},
		{"sin type", `{"value":{"state":1},"timestamp":1}`, "type"},
		{"sin value", `{"timestamp":1,"type":"SUSPEND_EVT"}`, "value"},
		{"JSON roto", `{"value":`, "ilegible"},
		{"vacío", ``, "ilegible"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			res, err := parseIgnitionEvents([]byte(c.json))
			if err == nil {
				t.Fatalf("debería dar error, devolvió %#v", res)
			}
			if res != nil {
				t.Errorf("con error no se devuelve mensaje, devolvió %#v", res)
			}
			if !strings.Contains(err.Error(), c.dice) {
				t.Errorf("el error debe mencionar %q: %s", c.dice, err)
			}
		})
	}
}
