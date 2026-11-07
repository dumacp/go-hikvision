package isapi

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDownloadEntregaElArchivoTalCual(t *testing.T) {
	const uri = "rtsp://192.168.186.93/Streaming/tracks/101/?starttime=20261107T092621Z&endtime=20261107T092838Z&name=ch01_x&size=34084248"
	contenido := "\x00\x00\x01\xba" + strings.Repeat("video", 1000)
	srv := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req downloadRequest
		body, _ := io.ReadAll(r.Body)
		if err := xml.Unmarshal(body, &req); err != nil {
			t.Fatalf("petición inválida: %v", err)
		}
		// El playbackURI tiene que llegar intacto, con name y size: sin ellos la G1 se cuelga.
		if req.PlaybackURI != uri {
			t.Errorf("playbackURI alterado: %q", req.PlaybackURI)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, contenido)
	})
	defer srv.Close()

	rc, err := clientFor(srv).Download(context.Background(), uri)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != contenido {
		t.Errorf("el contenido no llegó completo: %d bytes de %d", len(got), len(contenido))
	}
}

// Es lo que responde la G0: el modelo no tiene descarga y hay que usar el RTSP.
func TestDownloadNoSoportadaEsReconocible(t *testing.T) {
	srv := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ResponseStatus version="1.0">`+
			`<requestURL>/ISAPI/ContentMgmt/download</requestURL><statusCode>4</statusCode>`+
			`<statusString>Invalid Operation</statusString><subStatusCode>methodNotAllowed</subStatusCode></ResponseStatus>`)
	})
	defer srv.Close()

	_, err := clientFor(srv).Download(context.Background(), "rtsp://x")
	if !errors.Is(err, ErrDescargaNoSoportada) {
		t.Errorf("debía ser ErrDescargaNoSoportada, fue %v", err)
	}
}

func TestDownloadOtroErrorNoEsNoSoportada(t *testing.T) {
	srv := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	_, err := clientFor(srv).Download(context.Background(), "rtsp://x")
	if err == nil || errors.Is(err, ErrDescargaNoSoportada) {
		t.Errorf("un 500 es un fallo pasajero, no falta de soporte: %v", err)
	}
}
