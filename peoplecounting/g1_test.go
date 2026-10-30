package peoplecounting

import (
	"strings"
	"testing"
)

// xmlG1V422 es el evento real de la DS-2XM6825G1 con firmware V4.2.2, capturado del
// POST a :8088. Namespace isapi.org/ver20, pero eventType PeopleCounting y
// statisticalMethods como la G0: desde esa versión las dos generaciones hablan el
// MISMO dialecto y entran por un solo camino.
const xmlG1V422 = `<?xml version="1.0" encoding="UTF-8"?>
<EventNotificationAlert version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<ipAddress>192.168.186.93</ipAddress>
<portNo>80</portNo>
<protocol>HTTP</protocol>
<macAddress>5c:34:5b:10:32:77</macAddress>
<channelID>1</channelID>
<dateTime>2026-10-30T18:17:44-05:00</dateTime>
<activePostCount>1</activePostCount>
<eventType>PeopleCounting</eventType>
<eventState>active</eventState>
<eventDescription>People Counting</eventDescription>
<peopleCounting>
<statisticalMethods>realTime</statisticalMethods>
<RealTime><time>2026-10-30T18:17:44-05:00</time></RealTime>
<enter>21</enter>
<exit>20</exit>
<regionsID>1</regionsID>
</peopleCounting>
<childCounting><enter>0</enter><exit>0</exit></childCounting>
</EventNotificationAlert>`

// xmlG1V322 es el evento de la MISMA cámara con el firmware anterior: eventType
// "counting" y conteo por ventana. Ya no se parsea; está acá para fijar que el rechazo
// sea explícito y diga qué hacer.
const xmlG1V322 = `<?xml version="1.0" encoding="UTF-8"?>
<EventNotificationAlert version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<ipAddress>192.168.186.93</ipAddress>
<channelID>1</channelID>
<dateTime>2026-10-30T10:21:02-05:00</dateTime>
<eventType>counting</eventType>
<eventState>active</eventState>
<eventDescription>People Counting</eventDescription>
<peopleCounting>
<TimeRange><startTime>2026-10-30T10:05:59-05:00</startTime><endTime>2026-10-30T10:21:00-05:00</endTime></TimeRange>
<enter>2</enter>
<exit>3</exit>
</peopleCounting>
</EventNotificationAlert>`

// xmlG0 es el conteo por cruce de la DS-2XM6825G0. Está acá para que una prueba sola
// demuestre que las dos cámaras conviven.
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

// TestLasDosGeneracionesMismoTipo: con V4.2.2 la G1 y la G0 producen el MISMO tipo de
// evento. Es lo que permite un solo canal, un solo parser y una sola regla de conteo.
func TestLasDosGeneracionesMismoTipo(t *testing.T) {
	casos := []struct {
		nombre            string
		xml               string
		enter, exit       int64
		metodoEstadistico string
	}{
		{"G0", xmlG0, 17, 15, "realTime"},
		{"G1 con V4.2.2", xmlG1V422, 21, 20, "realTime"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ev, err := parseXMLEvent(strings.NewReader(c.xml))
			if err != nil {
				t.Fatalf("no debería fallar: %s", err)
			}
			p, ok := ev.(*EventNotificationAlertPeopleConting)
			if !ok {
				t.Fatalf("tipo %T, quiero *EventNotificationAlertPeopleConting", ev)
			}
			if p.PeopleCounting.Enter != c.enter || p.PeopleCounting.Exit != c.exit {
				t.Errorf("enter=%d exit=%d, quiero %d y %d",
					p.PeopleCounting.Enter, p.PeopleCounting.Exit, c.enter, c.exit)
			}
			if p.PeopleCounting.StatisticalMethods != c.metodoEstadistico {
				t.Errorf("statisticalMethods %q", p.PeopleCounting.StatisticalMethods)
			}
		})
	}
}

// TestEventoDeVentanaRechazado: el formato de la V3.2.2 ya no se parsea, y el error
// tiene que decir qué hacer. Se quitó porque no dice en qué instante cruzó cada
// persona —así que nunca puede tener video— y mantener vivo un camino que no se puede
// probar solo confunde el diagnóstico.
func TestEventoDeVentanaRechazado(t *testing.T) {
	ev, err := parseXMLEvent(strings.NewReader(xmlG1V322))
	if err == nil {
		t.Fatal("el conteo por ventana ya no se soporta: debe dar error")
	}
	if ev != nil {
		t.Errorf("con error el evento debe ser nil, es %T", ev)
	}
	// El mensaje es la mitad útil: sin la indicación de actualizar, quien lo vea en
	// campo no sabe que la solución es el firmware y no el binario.
	for _, debe := range []string{"counting", "V3.2.2", "V4.2.2"} {
		if !strings.Contains(err.Error(), debe) {
			t.Errorf("el error debe mencionar %q: %s", debe, err)
		}
	}
}

// TestEventTypeDesconocidoAvisa es el arreglo de observabilidad: un tipo que no
// conocemos tiene que dar error con su nombre, no desaparecer devolviendo (nil, nil).
// Ese silencio fue lo que hizo falta capturar tráfico en crudo para diagnosticar.
func TestEventTypeDesconocidoAvisa(t *testing.T) {
	raro := strings.Replace(xmlG1V422, "<eventType>PeopleCounting</eventType>",
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
