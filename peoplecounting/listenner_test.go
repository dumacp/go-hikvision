package peoplecounting

import (
	"bytes"
	"io"
	"reflect"
	"testing"
)

func Test_parseXMLEvent(t *testing.T) {
	type args struct {
		reader io.Reader
	}
	tests := []struct {
		name    string
		args    args
		want    interface{}
		wantErr bool
	}{
		// TODO: Add test cases.
		{
			"test-1",
			args{
				bytes.NewReader([]byte(`<?xml version="1.0" encoding="UTF-8"?>
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
</EventNotificationAlert>`))},
			nil,
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseXMLEvent(tt.args.reader)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseXMLEvent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseXMLEvent() = %#v, want %v", got, tt.want)
			}
		})
	}
}
