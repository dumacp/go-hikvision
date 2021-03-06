package peoplecounting

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

func parseXMLEvent(reader io.Reader) (interface{}, error) {
	decoder := xml.NewDecoder(reader)
	decoder.Token() // xml version
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
			case xml.CharData:
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

	fmt.Println(eventType)

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
	case ScenechangedetectionType:
		result = eventNotificationAlert
	case ShelteralarmType:
		result = eventNotificationAlert
	}

	return result, nil
}
