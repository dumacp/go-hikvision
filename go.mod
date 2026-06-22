module github.com/dumacp/go-hikvision

go 1.16

replace github.com/dumacp/go-doors => ../go-doors

replace github.com/dumacp/go-actors => ../go-actors

require (
	github.com/asynkron/protoactor-go v0.0.0-20230414121700-22ab527f4f7a
	github.com/brian-armstrong/gpio v0.0.0-20181227042754-72b0058bbbcb
	github.com/dumacp/go-actors v0.0.0-00010101000000-000000000000
	github.com/dumacp/go-doors v0.0.0-00010101000000-000000000000
	github.com/dumacp/go-logs v0.0.1
	github.com/dumacp/gpsnmea v0.0.0-20201110195359-2994f05cfb52
	github.com/dumacp/pubsub v0.0.0-20200115200904-f16f29d84ee0
	github.com/eclipse/paho.mqtt.golang v1.4.2
	github.com/gogo/protobuf v1.3.2
	github.com/tarm/serial v0.0.0-20180830185346-98f6abe2eb07 // indirect
	golang.org/x/exp/errors v0.0.0-20210916165020-5cb4fee858ee
	google.golang.org/protobuf v1.31.0
)

replace github.com/asynkron/protoactor-go => ../../asynkron/protoactor-go
