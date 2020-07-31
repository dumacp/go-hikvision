package peoplecounting

import (
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

type EventType struct {
	XMLName   xml.Name
	EventType string `xml:"eventType"`
}

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
				if strings.HasPrefix(v.Name.Local, "eventType") {
					var dm string
					if err := decoder.DecodeElement(&dm, &v); err != nil {
						fmt.Println(err)
					}
					fmt.Printf("\n\n%#v\n", dm)
				}
				if strings.HasPrefix(v.Name.Local, "eventType") {
					fmt.Println(name)
					if err := decoder.DecodeElement(&catch, &v); err != nil {
						return err
					}
					fmt.Printf("\n\n%#v\n", catch)
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
	if err := funcGetData(decoder, "dateTime", dateTime); err != nil {
		return nil, err
	}

	eventNotificationAlert.DateTime = dateTime

	var eventType string
	if err := funcGetData(decoder, "eventType", eventType); err != nil {
		return nil, err
	}

	fmt.Println(eventType)

	eventNotificationAlert.EventType = eventType

	// for i := 0; i < 30; i++ {
	// 	temp, _ := decoder.Token()

	// 	fmt.Printf("%#v", temp)
	// }

	if strings.HasPrefix(fmt.Sprintf("%s", eventType), peopleCountingType) {

		people := new(EventPeopleCounting)
		if err := funcGetData(decoder, "peopleCounting", people); err != nil {
			return nil, err
		}

		fmt.Printf("%#v\n", people)

		child := new(EventChildCounting)
		if err := funcGetData(decoder, "childPeople", child); err != nil {
			return nil, err
		}

		result = &EventNotificationAlertPeopleConting{
			eventNotificationAlert,
			0,
			people,
			child,
		}
	}
	return result, nil
}

func Listen(socket string) {

	h1 := func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPost:
			// disp := req.Header.Get("Content-Disposition")
			// _, params, err := mime.ParseMediaType(disp)
			// if err != nil {
			// 	return
			// }
			// name, ok := params["name"]
			// if !ok {
			// 	return
			// }
			// if ok := strings.Contains(name, "Event_Type"); !ok {
			// 	return
			// }
			ctype := req.Header.Get("Content-Type")
			if ok := strings.Contains(ctype, "application/xml"); !ok {
				reader, err := req.GetBody()
				if err != nil {
					return
				}
				parseXMLEvent(reader)

			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\": \"OK\"}"))
	}

	http.HandleFunc("/", h1)

	err := http.ListenAndServe(socket, nil)
	log.Fatal(err)
}
