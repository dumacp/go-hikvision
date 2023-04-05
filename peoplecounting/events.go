package peoplecounting

import "encoding/xml"

const (
	PeopleCountingType       = "PeopleCounting"
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
