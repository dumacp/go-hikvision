package isapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"
)

// Time is the device time configuration, XML_Time in ISAPI_general.pdf §16.11.229.
type Time struct {
	XMLName xml.Name `xml:"Time"`
	Version string   `xml:"version,attr,omitempty"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`

	// TimeMode is "NTP" or "manual" on the verified camera; the spec also lists
	// "local", "satellite" and "timecorrect".
	TimeMode string `xml:"timeMode"`
	// LocalTime is required when TimeMode is "manual" or "local".
	LocalTime string `xml:"localTime,omitempty"`
	// TimeZone is a POSIX string and required for "manual", "local" and "NTP".
	//
	// Careful: POSIX inverts the sign. "CST+5:00:00" means UTC-5, which is correct
	// for Colombia; writing "CST-5:00:00" leaves the camera ten hours off.
	TimeZone string `xml:"timeZone,omitempty"`
	// SatelliteInterval is in minutes and only valid with TimeMode "satellite".
	SatelliteInterval int `xml:"satelliteInterval,omitempty"`
}

// Parsed returns LocalTime as a time.Time, tolerating the offset quirks of the device.
func (t *Time) Parsed() (time.Time, error) {
	return tolerantTime(t.LocalTime)
}

// TimeCap is XML_Cap_Time: which modes this model accepts.
type TimeCap struct {
	XMLName  xml.Name `xml:"Time"`
	TimeMode struct {
		Opt string `xml:"opt,attr"`
	} `xml:"timeMode"`
	TimeType struct {
		Opt string `xml:"opt,attr"`
	} `xml:"timeType"`
}

// NTPServer is XML_NTPServer.
type NTPServer struct {
	XMLName xml.Name `xml:"NTPServer"`
	Version string   `xml:"version,attr,omitempty"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`

	ID                   string `xml:"id"`
	AddressingFormatType string `xml:"addressingFormatType"` // "ipaddress" or "hostname"
	HostName             string `xml:"hostName,omitempty"`
	IPAddress            string `xml:"ipAddress,omitempty"`
	PortNo               int    `xml:"portNo,omitempty"`
	// SynchronizeInterval is in MINUTES. A verified camera shipped with 1500, that
	// is 25 hours between syncs, which is the likeliest cause of a device that has
	// NTP configured and still drifts.
	SynchronizeInterval int `xml:"synchronizeInterval,omitempty"`
}

// Address returns the configured server, whichever field holds it.
func (s *NTPServer) Address() string {
	if len(s.HostName) > 0 {
		return s.HostName
	}
	return s.IPAddress
}

// NTPServerList is XML_NTPServerList.
type NTPServerList struct {
	XMLName xml.Name `xml:"NTPServerList"`
	Version string   `xml:"version,attr,omitempty"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`

	Servers []NTPServer `xml:"NTPServer"`
}

// NTPTestDescription is the body of the NTP availability test.
type NTPTestDescription struct {
	XMLName xml.Name `xml:"NTPTestDescription"`
	Version string   `xml:"version,attr,omitempty"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`

	AddressingFormatType string `xml:"addressingFormatType"`
	HostName             string `xml:"hostName,omitempty"`
	IPAddress            string `xml:"ipAddress,omitempty"`
	PortNo               int    `xml:"portNo"`
}

// NTPTestResult is the answer to the test. The spec only documents errorDescription,
// but the verified firmware also returns errorCode (0 with "ok").
type NTPTestResult struct {
	XMLName          xml.Name `xml:"NTPTestResult"`
	ErrorCode        int      `xml:"errorCode"`
	ErrorDescription string   `xml:"errorDescription"`
}

// Reachable reports whether the device could reach the NTP server.
func (r *NTPTestResult) Reachable() bool {
	return r.ErrorCode == 0 && (len(r.ErrorDescription) == 0 || r.ErrorDescription == "ok")
}

// GetTime reads GET /ISAPI/System/time.
func (c *Client) GetTime(ctx context.Context) (*Time, error) {
	out := new(Time)
	if err := c.get(ctx, "/ISAPI/System/time", out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutTime writes PUT /ISAPI/System/time.
//
// There is no "synchronize now" endpoint in ISAPI: re-applying the configuration is
// the only way to nudge the device into syncing. Writing LocalTime with TimeMode
// "manual" sets the clock but abandons NTP until it is restored.
func (c *Client) PutTime(ctx context.Context, in *Time) error {
	body := *in
	fillEnvelope(&body.Version, &body.Xmlns)
	return c.put(ctx, "/ISAPI/System/time", &body)
}

// GetTimeCapabilities reads GET /ISAPI/System/time/capabilities, to check the device
// accepts a mode before writing it.
func (c *Client) GetTimeCapabilities(ctx context.Context) (*TimeCap, error) {
	out := new(TimeCap)
	if err := c.get(ctx, "/ISAPI/System/time/capabilities", out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetNTPServers reads GET /ISAPI/System/time/ntpServers.
func (c *Client) GetNTPServers(ctx context.Context) (*NTPServerList, error) {
	out := new(NTPServerList)
	if err := c.get(ctx, "/ISAPI/System/time/ntpServers", out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutNTPServers writes PUT /ISAPI/System/time/ntpServers, replacing the whole list.
func (c *Client) PutNTPServers(ctx context.Context, in *NTPServerList) error {
	if len(in.Servers) == 0 {
		// A PUT with no servers would wipe the configuration; DELETE says that
		// explicitly, so refuse to do it by accident.
		return fmt.Errorf("isapi: refusing to PUT an empty NTP server list")
	}
	body := *in
	fillEnvelope(&body.Version, &body.Xmlns)
	body.Servers = make([]NTPServer, len(in.Servers))
	copy(body.Servers, in.Servers)
	for i := range body.Servers {
		// Nested elements must not repeat the namespace attribute.
		body.Servers[i].Version = ""
		body.Servers[i].Xmlns = ""
	}
	return c.put(ctx, "/ISAPI/System/time/ntpServers", &body)
}

// TestNTPServer runs POST /ISAPI/System/time/ntpServers/test.
//
// It only checks that the device can reach the server. It does NOT trigger a
// synchronization, which is worth remembering when diagnosing drift: a server that
// answers "ok" says nothing about when the camera last corrected its clock.
func (c *Client) TestNTPServer(ctx context.Context, in *NTPTestDescription) (*NTPTestResult, error) {
	body := *in
	fillEnvelope(&body.Version, &body.Xmlns)
	out := new(NTPTestResult)
	if err := c.post(ctx, "/ISAPI/System/time/ntpServers/test", &body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// fillEnvelope defaults the version and namespace attributes of a request. Callers
// that round trip a document read from the device keep its own namespace, which is
// what makes this work across the three variants seen in the field.
func fillEnvelope(version, xmlns *string) {
	if len(*version) == 0 {
		*version = Version
	}
	if len(*xmlns) == 0 {
		*xmlns = NamespaceHikvision
	}
}
