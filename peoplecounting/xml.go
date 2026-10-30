package peoplecounting

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

func parseXMLEvent(reader io.Reader) (interface{}, error) {
	decoder := xml.NewDecoder(reader)
	// Antes se consumia el primer token a ciegas, asumiendo que siempre era la
	// declaracion <?xml ?>. Un cuerpo que no la trajera perdia su elemento raiz y
	// fallaba con "it's not an Event", nombrando el segundo elemento — un mensaje que
	// manda a buscar el problema al lugar equivocado. Ahora la declaracion se salta si
	// esta y no se exige si no.
	funcVerifyEvent := func(decoder *xml.Decoder) error {
		for {
			el, err := decoder.Token()
			if err != nil {
				return err
			}
			switch v := el.(type) {
			case xml.StartElement:
				if !strings.HasPrefix(v.Name.Local, "EventNotificationAlert") {
					return fmt.Errorf("it's not an Event -> %s", v)
				}
				return nil
			case xml.CharData, xml.ProcInst, xml.Comment, xml.Directive:
			default:
				return fmt.Errorf("it's not an Event -> %+v, %T", v, v)
			}
		}
	}

	funcGetData := func(decoder *xml.Decoder, name string, catch interface{}) error {

		for {
			el, err := decoder.Token()
			if err != nil {
				if err != io.EOF {
					break
				} else {
					return err
				}
			}
			switch v := el.(type) {
			case xml.StartElement:
				if strings.HasPrefix(v.Name.Local, name) {
					// var dm string
					if err := decoder.DecodeElement(catch, &v); err != nil {
						fmt.Println(err)
					}
					// fmt.Printf("\n\n2 -> %+v\n", catch)
					return nil
				}

			default:
			}
		}
		return nil
	}

	if err := funcVerifyEvent(decoder); err != nil {
		return nil, err
	}

	var result interface{}
	eventNotificationAlert := new(EventNotificationAlert)

	var dateTime string
	if err := funcGetData(decoder, "dateTime", &dateTime); err != nil {
		return nil, err
	}

	eventNotificationAlert.DateTime = dateTime

	var eventType string
	if err := funcGetData(decoder, "eventType", &eventType); err != nil {
		return nil, err
	}

	eventNotificationAlert.EventType = eventType

	// for i := 0; i < 30; i++ {
	// 	temp, _ := decoder.Token()

	// 	fmt.Printf("%#v", temp)
	// }

	switch eventType {
	case PeopleCountingType:

		// if strings.HasPrefix(fmt.Sprintf("%s", eventType), peopleCountingType) {

		people := new(EventPeopleCounting)
		if err := funcGetData(decoder, "peopleCounting", people); err != nil {
			return nil, err
		}

		// fmt.Printf("%#v\n", people)

		child := new(EventChildCounting)
		if err := funcGetData(decoder, "childCounting", child); err != nil {
			return nil, err
		}

		result = &EventNotificationAlertPeopleConting{
			eventNotificationAlert,
			0,
			people,
			child,
		}
	case eventoVentanaG1:
		// La DS-2XM6825G1 con firmware V3.2.2 manda este eventType con el conteo de
		// una ventana de tiempo en vez del acumulado. Se SOPORTO y se quito: ese
		// formato no dice en que instante cruzo cada persona, asi que nunca puede
		// tener video, y mantener vivo un camino que no se puede probar solo sirve
		// para confundir el diagnostico. La salida es actualizar la camara: desde
		// V4.2.2 manda PeopleCounting con acumulados, igual que la G0.
		return nil, fmt.Errorf("eventType %q: la camara esta en firmware V3.2.2 y "+
			"reporta por ventana, no por cruce. Actualizala a V4.2.2 o posterior",
			eventType)
	case ScenechangedetectionType:
		result = eventNotificationAlert
	case ShelteralarmType:
		result = eventNotificationAlert
	default:
		// Un eventType que no conocemos se descartaba en silencio, y la camara recibia
		// un 200: desde los dos lados el sistema se veia sano mientras el conteo
		// simplemente no aparecia. Asi se perdio el conteo de la G1 durante dias, hasta
		// que hubo que capturar los POST en crudo para verlo.
		//
		// Es el mismo defecto que se corrigio en 1.0.30 para el Content-Type, donde el
		// WARN al descartar fue la mitad valiosa del arreglo. Devolver el error deja que
		// quien llama registre el tipo y la direccion de origen.
		return nil, fmt.Errorf("eventType %q desconocido, evento descartado", eventType)
	}

	return result, nil
}
