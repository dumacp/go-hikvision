package peoplecounting

import (
	"strings"
	"testing"
)

// xmlG1 es el evento real de la DS-2XM6825G1 (firmware V3.2.2), capturado del POST a
// :8088 en banco. Namespace isapi.org/ver20, eventType "counting", conteo por ventana.
const xmlG1 = `<?xml version="1.0" encoding="UTF-8"?>
<EventNotificationAlert version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<ipAddress>192.168.186.93</ipAddress>
<portNo>80</portNo>
<protocol>HTTP</protocol>
<macAddress>5c:34:5b:10:32:77</macAddress>
<dynChannelID>1</dynChannelID>
<channelID>1</channelID>
<dateTime>2026-10-30T10:21:02-05:00</dateTime>
<activePostCount>1</activePostCount>
<eventType>counting</eventType>
<eventState>active</eventState>
<eventDescription>People Counting</eventDescription>
<peopleCounting>
<TimeRange><startTime>2026-10-30T10:05:59-05:00</startTime><endTime>2026-10-30T10:21:00-05:00</endTime></TimeRange>
<enter>2</enter>
<exit>3</exit>
</peopleCounting>
<Extensions><serialNumber></serialNumber><eventPush>,2026-10-30T10:21:00-05:00,</eventPush></Extensions>
</EventNotificationAlert>`

// xmlG0 es el conteo por cruce de la DS-2XM6825G0, el que ya funcionaba. Está acá para
// que una prueba sola demuestre que las dos cámaras conviven.
const xmlG0 = `<?xml version="1.0" encoding="UTF-8"?>
<EventNotificationAlert version="1.0" xmlns="urn:psialliance-org">
<ipAddress>192.168.186.91</ipAddress>
<channelID>1</channelID>
<dateTime>2026-10-30T10:21:02-5:00</dateTime>
<eventType>PeopleCounting</eventType>
<eventState>active</eventState>
<eventDescription>peopleCounting alarm</eventDescription>
<peopleCounting>
<statisticalMethods>realTime</statisticalMethods>
<RealTime><time>2026-10-30T10:21:02-5:00</time></RealTime>
<enter>17</enter>
<exit>15</exit>
<regionsID>1</regionsID>
</peopleCounting>
<childCounting><enter>0</enter><exit>0</exit></childCounting>
</EventNotificationAlert>`

// TestParseG1Counting: el evento de la G1 se reconoce y entrega la ventana con sus
// incrementos. Antes caía en el switch sin rama y devolvía (nil, nil).
func TestParseG1Counting(t *testing.T) {
	ev, err := parseXMLEvent(strings.NewReader(xmlG1))
	if err != nil {
		t.Fatalf("no debería fallar: %s", err)
	}
	c, ok := ev.(*EventNotificationAlertCounting)
	if !ok {
		t.Fatalf("tipo %T, quiero *EventNotificationAlertCounting", ev)
	}
	if c.EventType != CountingType {
		t.Errorf("eventType %q", c.EventType)
	}
	if c.Counting.Enter != 2 || c.Counting.Exit != 3 {
		t.Errorf("enter=%d exit=%d, quiero 2 y 3", c.Counting.Enter, c.Counting.Exit)
	}
	// La ventana es lo que distingue este evento del de la G0: sin ella no se puede
	// saber que el conteo es de un intervalo y no de un instante.
	if c.Counting.TimeRange.StartTime != "2026-10-30T10:05:59-05:00" {
		t.Errorf("startTime %q", c.Counting.TimeRange.StartTime)
	}
	if c.Counting.TimeRange.EndTime != "2026-10-30T10:21:00-05:00" {
		t.Errorf("endTime %q", c.Counting.TimeRange.EndTime)
	}
}

// TestParseG0SigueIgual es la mitad que protege lo que ya funcionaba: la G0 no puede
// cambiar de comportamiento por haber agregado soporte a la G1.
func TestParseG0SigueIgual(t *testing.T) {
	ev, err := parseXMLEvent(strings.NewReader(xmlG0))
	if err != nil {
		t.Fatalf("no debería fallar: %s", err)
	}
	p, ok := ev.(*EventNotificationAlertPeopleConting)
	if !ok {
		t.Fatalf("tipo %T, quiero *EventNotificationAlertPeopleConting", ev)
	}
	if p.PeopleCounting.Enter != 17 || p.PeopleCounting.Exit != 15 {
		t.Errorf("enter=%d exit=%d, quiero 17 y 15", p.PeopleCounting.Enter, p.PeopleCounting.Exit)
	}
	if p.PeopleCounting.StatisticalMethods != "realTime" {
		t.Errorf("statisticalMethods %q", p.PeopleCounting.StatisticalMethods)
	}
}

// TestEventTypeDesconocidoAvisa es el arreglo de observabilidad: un tipo que no
// conocemos tiene que dar error con su nombre, no desaparecer devolviendo (nil, nil).
// Ese silencio fue lo que hizo falta capturar tráfico en crudo para diagnosticar la G1.
func TestEventTypeDesconocidoAvisa(t *testing.T) {
	raro := strings.Replace(xmlG1, "<eventType>counting</eventType>",
		"<eventType>algoNuevoDeHikvision</eventType>", 1)
	ev, err := parseXMLEvent(strings.NewReader(raro))
	if err == nil {
		t.Fatal("un eventType desconocido debe dar error, no pasar en silencio")
	}
	if ev != nil {
		t.Errorf("con error el evento debe ser nil, es %T", ev)
	}
	if !strings.Contains(err.Error(), "algoNuevoDeHikvision") {
		t.Errorf("el error debe nombrar el tipo recibido: %s", err)
	}
}
