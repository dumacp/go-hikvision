module github.com/dumacp/go-hikvision

go 1.16

replace github.com/dumacp/go-actors => ../go-actors

replace github.com/dumacp/go-doors => ../go-doors

replace github.com/dumacp/go-logs => ../go-logs

require (
	github.com/AsynkronIT/protoactor-go v0.0.0-20210927041136-0024968a0dd3
	github.com/brian-armstrong/gpio v0.0.0-20181227042754-72b0058bbbcb
	github.com/dumacp/go-actors v0.0.0-20210923182122-b64616cc9d17
	github.com/dumacp/go-doors v0.0.0-00010101000000-000000000000
	github.com/dumacp/go-logs v0.0.0-00010101000000-000000000000
	github.com/dumacp/gpsnmea v0.0.0-20201110195359-2994f05cfb52
	github.com/dumacp/pubsub v0.0.0-20200115200904-f16f29d84ee0
	github.com/eclipse/paho.mqtt.golang v1.4.1
	github.com/gogo/protobuf v1.3.2
	github.com/golang/protobuf v1.5.2
	github.com/mkch/gpio v0.0.0-20190919032813-8327cd97d95e
	github.com/tarm/serial v0.0.0-20180830185346-98f6abe2eb07 // indirect
	golang.org/x/exp/errors v0.0.0-20210916165020-5cb4fee858ee
)
