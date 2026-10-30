package peoplecounting

import "encoding/xml"

const (
	// PeopleCountingType es el conteo de la DS-2XM6825G0 (esquema ISAPI 1.0,
	// urn:psialliance-org). Trae ACUMULADOS del dia y, con statisticalMethods
	// "realTime", llega en cada cruce.
	PeopleCountingType = "PeopleCounting"
	// CountingType es el conteo de la DS-2XM6825G1 (esquema ISAPI 2.0,
	// http://www.isapi.org/ver20/XMLSchema), verificado en firmware V3.2.2.
	//
	// Es OTRO evento, no una variante del anterior: trae el conteo de una VENTANA de
	// tiempo (<TimeRange>) y no el acumulado, y llega una vez por `dataUploadCycle`
	// (15 min por defecto) en vez de en cada cruce. Ese modelo no esta en la guia
	// oficial de Hikvision: no aparece "counting" como eventType en
	// ISAPI_peopleCounting.pdf ni en ISAPI_general.pdf.
	//
	// La G1 ignora el ajuste que deberia dar tiempo real. La propia guia describe
	// <SECUploadEnabled> como "uploading mechanism for each second", y la camara lo
	// acepta en true sin honrarlo.
	CountingType             = "counting"
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

// EventWindowCounting es el conteo por ventana de la G1.
//
// Enter y Exit son INCREMENTOS de la ventana [StartTime, EndTime], no acumulados. Va en
// un tipo propio y no reutilizando EventPeopleCounting a proposito: comparten los
// nombres enter/exit pero significan cosas distintas, y confundirlos corrompe el
// acumulado en cascada. Es el mismo motivo por el que ListenActor descarta los
// PeopleCounting con statisticalMethods "timeRange" o "signalTrigger".
type EventWindowCounting struct {
	XMLName   xml.Name  `xml:"peopleCounting"`
	TimeRange TimeRange `xml:"TimeRange"`
	Enter     int64     `xml:"enter"`
	Exit      int64     `xml:"exit"`
}

// EventNotificationAlertCounting es el evento completo de la G1.
type EventNotificationAlertCounting struct {
	*EventNotificationAlert
	Counting *EventWindowCounting `xml:"peopleCounting"`
}
