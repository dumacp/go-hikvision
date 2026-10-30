package peoplecounting

import (
	"strings"
	"testing"
)

// Test_parseXMLEvent cubre el reconocimiento del tipo de evento y la forma del cuerpo.
// Los valores de conteo de cada generacion se verifican en g1_test.go; aca esta lo que
// no depende del modelo de camara: que cada eventType caiga en su rama, que el tamper
// no pretenda traer conteo, y que un cuerpo mal formado falle con un mensaje util.
func Test_parseXMLEvent(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		// check corre solo cuando no se espera error.
		check func(t *testing.T, got interface{})
	}{
		{
			// El evento de la guia oficial, con offset +08:00 y namespace psialliance.
			// Es el que estaba en el esqueleto de gotests y nunca se completo.
			name: "PeopleCounting de la G0",
			body: `<?xml version="1.0" encoding="UTF-8"?>
<EventNotificationAlert version="1.0" xmlns="urn:psialliance-org">
<ipAddress>192.168.1.64</ipAddress>
<protocolType>HTTP</protocolType>
<macAddress>84:9a:40:ec:f1:6d</macAddress>
<channelID>1</channelID>
<dateTime>2020-07-09T12:07:07+08:00</dateTime>
<activePostCount>1</activePostCount>
<eventType>PeopleCounting</eventType>
<eventState>active</eventState>
<eventDescription>peopleCounting alarm</eventDescription>
<channelName>Camera 01</channelName>
<peopleCounting>
<statisticalMethods>realTime</statisticalMethods>
<RealTime>
<time>2020-07-09T12:07:07+08:00</time>
</RealTime>
<enter>70</enter>
<exit>75</exit>
<regionsID>1</regionsID>
</peopleCounting>
<childCounting>
<enter>0</enter>
<exit>0</exit>
</childCounting>
</EventNotificationAlert>`,
			check: func(t *testing.T, got interface{}) {
				ev, ok := got.(*EventNotificationAlertPeopleConting)
				if !ok {
					t.Fatalf("tipo %T, quiero *EventNotificationAlertPeopleConting", got)
				}
				if ev.PeopleCounting.Enter != 70 || ev.PeopleCounting.Exit != 75 {
					t.Errorf("enter=%d exit=%d, quiero 70 y 75",
						ev.PeopleCounting.Enter, ev.PeopleCounting.Exit)
				}
				// El dateTime sale crudo del XML: normalizarlo es trabajo de
				// parseDateTime en el cliente, no del parser.
				if ev.DateTime != "2020-07-09T12:07:07+08:00" {
					t.Errorf("dateTime %q", ev.DateTime)
				}
				if ev.ChildCounting == nil {
					t.Error("childCounting no deberia quedar nil")
				}
			},
		},
		{
			// El tamper de la G0. Cae en su rama y devuelve el alert pelado: no hay
			// conteo que extraer, y pedirselo seria inventar un cero.
			name: "scenechangedetection de la G0",
			body: `<?xml version="1.0" encoding="UTF-8"?>
<EventNotificationAlert version="1.0" xmlns="urn:psialliance-org">
<channelID>1</channelID>
<dateTime>2026-10-30T10:21:02-5:00</dateTime>
<eventType>scenechangedetection</eventType>
<eventState>active</eventState>
<eventDescription>scenechangedetection alarm</eventDescription>
</EventNotificationAlert>`,
			check: func(t *testing.T, got interface{}) {
				ev, ok := got.(*EventNotificationAlert)
				if !ok {
					t.Fatalf("tipo %T, quiero *EventNotificationAlert", got)
				}
				if ev.EventType != ScenechangedetectionType {
					t.Errorf("eventType %q", ev.EventType)
				}
			},
		},
		{
			// El tamper de la G1 es OTRO nombre para la misma idea. Si esta rama
			// faltara, la G1 no tendria deteccion de manipulacion y nadie lo notaria:
			// no hay nada que contar que se vea faltar.
			name: "shelteralarm de la G1",
			body: `<?xml version="1.0" encoding="UTF-8"?>
<EventNotificationAlert version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<channelID>1</channelID>
<dateTime>2026-10-30T10:21:02-05:00</dateTime>
<eventType>shelteralarm</eventType>
<eventState>active</eventState>
<eventDescription>shelteralarm</eventDescription>
</EventNotificationAlert>`,
			check: func(t *testing.T, got interface{}) {
				ev, ok := got.(*EventNotificationAlert)
				if !ok {
					t.Fatalf("tipo %T, quiero *EventNotificationAlert", got)
				}
				if ev.EventType != ShelteralarmType {
					t.Errorf("eventType %q", ev.EventType)
				}
			},
		},
		{
			// La declaracion <?xml ?> es OPCIONAL. Antes se consumia el primer token a
			// ciegas asumiendo que estaba, asi que un cuerpo sin ella perdia su
			// elemento raiz y fallaba nombrando el segundo — mandando a buscar el
			// problema al lugar equivocado.
			name: "sin declaracion <?xml ?>",
			body: `<EventNotificationAlert version="1.0" xmlns="urn:psialliance-org">
<channelID>1</channelID>
<dateTime>2026-10-30T10:21:02-5:00</dateTime>
<eventType>PeopleCounting</eventType>
<peopleCounting>
<statisticalMethods>realTime</statisticalMethods>
<enter>4</enter>
<exit>1</exit>
<regionsID>1</regionsID>
</peopleCounting>
<childCounting><enter>0</enter><exit>0</exit></childCounting>
</EventNotificationAlert>`,
			check: func(t *testing.T, got interface{}) {
				ev, ok := got.(*EventNotificationAlertPeopleConting)
				if !ok {
					t.Fatalf("tipo %T, quiero *EventNotificationAlertPeopleConting", got)
				}
				if ev.PeopleCounting.Enter != 4 || ev.PeopleCounting.Exit != 1 {
					t.Errorf("enter=%d exit=%d, quiero 4 y 1",
						ev.PeopleCounting.Enter, ev.PeopleCounting.Exit)
				}
			},
		},
		{
			// Un XML valido que no es un evento. Vale la pena fijarlo porque la camara
			// tambien responde XML a las consultas ISAPI: un cuerpo cruzado tiene que
			// doler aca y no mas adelante.
			name:    "XML que no es un evento",
			body:    `<?xml version="1.0" encoding="UTF-8"?><ResponseStatus><statusCode>1</statusCode></ResponseStatus>`,
			wantErr: true,
		},
		{
			name:    "cuerpo vacio",
			body:    ``,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseXMLEvent(strings.NewReader(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseXMLEvent() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				// Con error el evento tiene que ser nil: devolver los dos no-nil
				// deja que quien llama encole un evento que no se pudo parsear.
				if got != nil {
					t.Errorf("con error el evento debe ser nil, es %T", got)
				}
				return
			}
			if got == nil {
				t.Fatal("sin error el evento no puede ser nil")
			}
			tt.check(t, got)
		})
	}
}
