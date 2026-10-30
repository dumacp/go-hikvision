package video

import (
	"strings"
	"testing"
	"time"
)

// bogota es el huso del despliegue: UTC−5 sin horario de verano.
var bogota = time.FixedZone("-05", -5*60*60)

// TestPlaybackURLUTC fija la convención histórica, la que usan las cámaras en campo.
// Si esto cambia, se rompe la extracción de los equipos ya desplegados.
//
// Ojo al `:554` que NO aparece: el host entra sin puerto porque filepath.Ext("1.2.3.4")
// devuelve ".3.4" y la condición que lo agregaría nunca se cumple con una IP. Es
// inofensivo —gortsplib usa 554 por defecto— pero está fijado acá para que se vea que es
// lo que el código hace hoy, no un descuido de la prueba.
func TestPlaybackURLUTC(t *testing.T) {
	req := Request{
		Host:  "192.168.186.91",
		Track: 101,
		Start: time.Date(2026, 10, 30, 17, 53, 58, 0, bogota),
	}
	got := req.PlaybackURL()
	const quiero = "rtsp://192.168.186.91/Streaming/tracks/101/?starttime=20261030T225358Z"
	if got != quiero {
		t.Errorf("\n got %s\nwant %s", got, quiero)
	}
}

// TestPlaybackURLLocal fija la convención de la G1 con V4.2.2: el MISMO instante se
// pide con el reloj de pared, cinco horas antes en el texto.
//
// Es el nudo del asunto: las dos URLs llevan sufijo Z y solo una de las dos cámaras
// entiende cada una. La diferencia de cinco horas es exactamente lo que hacía que la
// G1 respondiera 500 como si no tuviera grabación.
func TestPlaybackURLLocal(t *testing.T) {
	req := Request{
		Host:      "192.168.186.93",
		Track:     101,
		Start:     time.Date(2026, 10, 30, 17, 53, 58, 0, bogota),
		HoraLocal: true,
	}
	got := req.PlaybackURL()
	const quiero = "rtsp://192.168.186.93/Streaming/tracks/101/?starttime=20261030T175358Z"
	if got != quiero {
		t.Errorf("\n got %s\nwant %s", got, quiero)
	}
}

// TestPlaybackURLMismoInstante deja escrito que las dos formas describen el MISMO
// momento y difieren solo en el texto. Sin esto, alguien podría "arreglar" la de arriba
// moviendo el instante en vez de la convención.
func TestPlaybackURLMismoInstante(t *testing.T) {
	inicio := time.Date(2026, 10, 30, 17, 53, 58, 0, bogota)
	utc := Request{Host: "h", Track: 101, Start: inicio}.PlaybackURL()
	loc := Request{Host: "h", Track: 101, Start: inicio, HoraLocal: true}.PlaybackURL()
	if utc == loc {
		t.Fatal("con huso distinto de UTC las dos URLs tienen que diferir")
	}
	hora := func(u string) string {
		i := strings.Index(u, "starttime=")
		return u[i+len("starttime="):]
	}
	if hora(utc) != "20261030T225358Z" || hora(loc) != "20261030T175358Z" {
		t.Errorf("utc=%s local=%s", hora(utc), hora(loc))
	}
}

// TestPlaybackURLEnUTCCoinciden: con la cámara en UTC las dos convenciones dan la misma
// URL. Por eso Extract no reintenta en ese caso — seria pagar dos veces el mismo fallo.
func TestPlaybackURLEnUTCCoinciden(t *testing.T) {
	inicio := time.Date(2026, 10, 30, 22, 53, 58, 0, time.UTC)
	utc := Request{Host: "h", Track: 101, Start: inicio}.PlaybackURL()
	loc := Request{Host: "h", Track: 101, Start: inicio, HoraLocal: true}.PlaybackURL()
	if utc != loc {
		t.Errorf("en UTC deberian coincidir:\n %s\n %s", utc, loc)
	}
}
