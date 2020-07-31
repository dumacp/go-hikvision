package peoplecounting

import "encoding/xml"

const (
	peopleCountingType   = "peopleCounting"
	scenechangedetection = "scenechangedetection"
)

type TimeRange struct {
	StartTime string `xml:"startTime"`
	EndTime   string `xml:"endTime"`
}

type RealTime struct {
	Time string `xml:"time"`
}

type EventChildCounting struct {
	Enter int `xml:"enter"`
	Exit  int `xml:"exit"`
}

type EventPeopleCounting struct {
	XMLName xml.Name `xml:"peopleCounting"`
	// Xmlns              string `xml:"xmlns,attr"`
	StatisticalMethods string `xml:"statisticalMethods"`
	// RealTime           RealTime
	// TimeRange          TimeRange
	Enter int `xml:"enter"`
	Exit  int `xml:"exit"`
	// Pass      int `xml:"pass,chardata"`
	RegionsID int `xml:"regionsID"`
}

type EventNotificationAlert struct {
	XMLName          xml.Name `xml:"EventNotificationAlert"`
	ChannelID        int      `xml:"channelID"`
	DateTime         string   `xml:"dateTime"`
	EventType        string   `xml:"eventType"`
	EventDescription string   `xml:"eventDescription"`
}

type EventNotificationAlertPeopleConting struct {
	*EventNotificationAlert
	DuplicatePeople int                  `xml:"duplicatePeople"`
	PeopleCounting  *EventPeopleCounting `xml:"peopleCounting"`
	ChildCounting   *EventChildCounting  `xml:"childCounting"`
}
