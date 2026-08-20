//go:build ignore

// sniff se suscribe al broker MQTT local y muestra lo que publica el binario,
// para verificar el contrato sin depender de mosquitto_sub instalado.
//
//	go run .claude/skills/mqtt-contract/scripts/sniff.go [-broker tcp://127.0.0.1:1883] [-topics '#']
//
// Usa el mismo cliente paho que el binario, así que resuelve desde el go.mod del
// repo y no hace falta instalar nada. La etiqueta "//go:build ignore" evita que
// entre en cualquier build del proyecto.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	broker := flag.String("broker", "tcp://127.0.0.1:1883", "broker MQTT")
	topics := flag.String("topics", "#", "tópicos separados por coma")
	flag.Parse()

	buff := make([]byte, 4)
	rand.Read(buff)
	opt := mqtt.NewClientOptions().AddBroker(*broker).
		SetClientID("sniff-" + hex.EncodeToString(buff)).
		SetKeepAlive(30 * time.Second)

	c := mqtt.NewClient(opt)
	tk := c.Connect()
	if !tk.WaitTimeout(10*time.Second) || tk.Error() != nil {
		fmt.Fprintf(os.Stderr, "no se pudo conectar a %s: %v\n", *broker, tk.Error())
		os.Exit(1)
	}
	defer c.Disconnect(300)

	n := 0
	handler := func(_ mqtt.Client, m mqtt.Message) {
		n++
		fmt.Printf("%s  [%s] %s\n", time.Now().Format("15:04:05.000"), m.Topic(), m.Payload())
	}

	for _, t := range strings.Split(*topics, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if tk := c.Subscribe(t, 0, handler); !tk.WaitTimeout(5*time.Second) || tk.Error() != nil {
			fmt.Fprintf(os.Stderr, "error suscribiendo a %q: %v\n", t, tk.Error())
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "suscrito a %q\n", t)
	}

	fin := make(chan os.Signal, 1)
	signal.Notify(fin, syscall.SIGINT, syscall.SIGTERM)
	<-fin
	fmt.Fprintf(os.Stderr, "\n%d mensajes recibidos\n", n)
}
