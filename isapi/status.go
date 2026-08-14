package isapi

import (
	"context"
	"encoding/xml"
	"fmt"
)

// NamespaceHikvision is the namespace the verified firmware uses in its answers.
//
// Three different ones show up on a single device: ISAPI GET responses and the
// alertStream use this one, the event push uses "urn:psialliance-org", and the PDF
// documents "http://www.isapi.org/ver20/XMLSchema". Requests echo back whatever the
// device sent, and fall back to this value.
const NamespaceHikvision = "http://www.hikvision.com/ver20/XMLSchema"

// Version is the message version attribute used when the device did not supply one.
const Version = "2.0"

// ResponseStatus is the generic answer to a write, and can also come back with an
// error while the HTTP status is 200.
type ResponseStatus struct {
	XMLName       xml.Name `xml:"ResponseStatus"`
	RequestURL    string   `xml:"requestURL"`
	StatusCode    int      `xml:"statusCode"`
	StatusString  string   `xml:"statusString"`
	SubStatusCode string   `xml:"subStatusCode"`
	ErrorCode     int      `xml:"errorCode"`
	ErrorMsg      string   `xml:"errorMsg"`
}

// OK reports success. Both 0 and 1 mean OK per ISAPI_peopleCounting.pdf appendix A.
func (r *ResponseStatus) OK() bool {
	return r.StatusCode == 0 || r.StatusCode == 1
}

// Error lets a failed ResponseStatus travel as an error.
func (r *ResponseStatus) Error() string {
	msg := fmt.Sprintf("device reported statusCode %d (%s)", r.StatusCode, r.StatusString)
	if len(r.SubStatusCode) > 0 {
		msg += ", subStatusCode " + r.SubStatusCode
	}
	if len(r.ErrorMsg) > 0 {
		msg += ", " + r.ErrorMsg
	}
	return msg
}

// DeviceInfo identifies the camera; useful to record which model a measurement came
// from, since the endpoint set depends on model and firmware.
type DeviceInfo struct {
	XMLName         xml.Name `xml:"DeviceInfo"`
	DeviceName      string   `xml:"deviceName"`
	Model           string   `xml:"model"`
	SerialNumber    string   `xml:"serialNumber"`
	MACAddress      string   `xml:"macAddress"`
	FirmwareVersion string   `xml:"firmwareVersion"`
	DeviceType      string   `xml:"deviceType"`
}

// GetDeviceInfo reads GET /ISAPI/System/deviceInfo.
func (c *Client) GetDeviceInfo(ctx context.Context) (*DeviceInfo, error) {
	out := new(DeviceInfo)
	if err := c.get(ctx, "/ISAPI/System/deviceInfo", out); err != nil {
		return nil, err
	}
	return out, nil
}
