#!/bin/bash
#protoc -I=. -I=$GOPATH/src --gogoslick_out=plugins=grpc:. messages.proto
protoc -I=./ -I=$GOROOT/src --go_out=. --go_opt=paths=source_relative ./msgcamera.proto
