//go:build ignore

// capture-events registra crudo lo que la cámara Hikvision empuja por HTTP:
// método, ruta, TODOS los headers y el cuerpo sin interpretar. Responde igual
// que el listener real del binario (200 + {"status": "OK"}) para que la cámara
// no reintente ni marque el host como caído.
//
//	go run capture-events.go -socket :8088 [-out captura.log]
//
// Sirve para confirmar contra un equipo real, sin suponer nada:
//   - el Content-Type que realmente envía (¿application/xml o text/xml?)
//   - si el cuerpo es multipart (cámara con envío de imagen habilitado)
//   - el valor real de statisticalMethods (realTime, timeRange, signalTrigger)
//   - si viene eventState y con qué valor (active/inactive)
//
// El directorio .claude/ es ignorado por la herramienta de Go, y la etiqueta
// "//go:build ignore" evita que este archivo entre en cualquier build del repo.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

func main() {
	socket := flag.String("socket", ":8088", "dirección de escucha, igual que el -socket del binario")
	out := flag.String("out", "", "además de stdout, escribe a este archivo")
	flag.Parse()

	w := io.Writer(os.Stdout)
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalln(err)
		}
		defer f.Close()
		w = io.MultiWriter(os.Stdout, f)
	}

	n := 0
	http.HandleFunc("/", func(rw http.ResponseWriter, req *http.Request) {
		n++
		// Límite defensivo: con envío de imagen habilitado el cuerpo trae binario
		// de varios MB. El listener real del binario hoy no acota nada.
		body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
		req.Body.Close()

		fmt.Fprintf(w, "\n===== evento #%d  %s  desde %s =====\n",
			n, time.Now().Format(time.RFC3339), req.RemoteAddr)
		fmt.Fprintf(w, "%s %s %s\n", req.Method, req.URL.RequestURI(), req.Proto)

		keys := make([]string, 0, len(req.Header))
		for k := range req.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s: %s\n", k, strings.Join(req.Header[k], ", "))
		}

		ctype := req.Header.Get("Content-Type")
		fmt.Fprintf(w, "\n  -- diagnóstico --\n")
		fmt.Fprintf(w, "  Content-Type crudo:            %q\n", ctype)
		fmt.Fprintf(w, "  ¿pasa el filtro actual?        %v   (exige \"application/xml\")\n",
			strings.Contains(ctype, "application/xml"))
		fmt.Fprintf(w, "  ¿es multipart?                 %v\n", strings.Contains(ctype, "multipart"))
		fmt.Fprintf(w, "  bytes leídos:                  %d (err=%v)\n", len(body), err)
		for _, tag := range []string{"eventType", "eventState", "statisticalMethods", "enter", "exit"} {
			if v, ok := entre(body, tag); ok {
				fmt.Fprintf(w, "  <%s>%s\n", tag, v)
			} else {
				fmt.Fprintf(w, "  <%s> AUSENTE\n", tag)
			}
		}

		fmt.Fprintf(w, "\n  -- cuerpo crudo --\n%s\n", body)

		rw.WriteHeader(http.StatusOK)
		rw.Write([]byte(`{"status": "OK"}`))
	})

	fmt.Fprintf(w, "escuchando en %s — Ctrl-C para terminar\n", *socket)
	log.Fatalln(http.ListenAndServe(*socket, nil))
}

// entre extrae el contenido del primer <tag>...</tag>, sin parsear XML: aquí
// interesa ver lo que llegó, no validarlo.
func entre(body []byte, tag string) (string, bool) {
	s := string(body)
	i := strings.Index(s, "<"+tag+">")
	if i < 0 {
		return "", false
	}
	i += len(tag) + 2
	j := strings.Index(s[i:], "</"+tag+">")
	if j < 0 {
		return "", false
	}
	return strings.TrimSpace(s[i : i+j]), true
}
