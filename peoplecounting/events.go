package peoplecounting

import "encoding/xml"

const (
	// PeopleCountingType es el conteo de la DS-2XM6825G0 (esquema ISAPI 1.0,
	// urn:psialliance-org). Trae ACUMULADOS del dia y, con statisticalMethods
	// "realTime", llega en cada cruce.
	PeopleCountingType = "PeopleCounting"
	// eventoVentanaG1 es el eventType de la DS-2XM6825G1 con firmware V3.2.2, que
	// reportaba por VENTANA de tiempo y no por cruce. Ya no se parsea: se reconoce
	// solo para poder dar un error que diga que hay que actualizar la camara.
	eventoVentanaG1          = "counting"
	ScenechangedetectionType = "scenechangedetection"
	ShelteralarmType         = "shelteralarm"
)

// TimeRange struct
type TimeRange struct {
	StartTime string `xml:"startTime"`
	EndTime   string `xml:"endTime"`
}

// RealTime struct
type RealTime struct {
	Time string `xml:"time"`
}

// EventChildCounting struct
type EventChildCounting struct {
	XMLName xml.Name `xml:"childCounting"`
	Enter   int64    `xml:"enter"`
	Exit    int64    `xml:"exit"`
}

// EventPeopleCounting struct
type EventPeopleCounting struct {
	XMLName xml.Name `xml:"peopleCounting"`
	// Xmlns              string `xml:"xmlns,attr"`
	StatisticalMethods string `xml:"statisticalMethods"`
	// RealTime           RealTime
	// TimeRange          TimeRange
	Enter int64 `xml:"enter"`
	Exit  int64 `xml:"exit"`
	// Pass      int `xml:"pass,chardata"`
	RegionsID int `xml:"regionsID"`
}

// EventNotificationAlert struct
type EventNotificationAlert struct {
	XMLName          xml.Name `xml:"EventNotificationAlert"`
	ChannelID        int      `xml:"channelID"`
	DateTime         string   `xml:"dateTime"`
	EventType        string   `xml:"eventType"`
	EventDescription string   `xml:"eventDescription"`
}

// EventNotificationAlertPeopleConting struct
type EventNotificationAlertPeopleConting struct {
	*EventNotificationAlert
	DuplicatePeople int                  `xml:"duplicatePeople"`
	PeopleCounting  *EventPeopleCounting `xml:"peopleCounting"`
	ChildCounting   *EventChildCounting  `xml:"childCounting"`
}
