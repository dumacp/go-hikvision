package video

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
)

// muestraG1 son los primeros ~4.6 s de un archivo real bajado de la DS-2XM6825G1 (V4.2.2)
// por /ISAPI/ContentMgmt/download, el 2026-11-07: 56 cuadros H.265 a ~12 fps, con un
// keyframe en 0 s y otro en 4.165 s. Se quitaron los datos privados de Hikvision (0xBD),
// que en el original pesan 20 veces más que el video, y se dejaron los pack headers, el
// system header y el program stream map tal cual.
func muestraG1(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "g1_v422_gop.ps"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLectorPSDeLaG1(t *testing.T) {
	l := nuevoLectorPS(bytes.NewReader(muestraG1(t)))
	var cuadros []unidadPS
	if err := l.recorrer(func(u unidadPS) error { cuadros = append(cuadros, u); return nil }); err != nil {
		t.Fatal(err)
	}
	if l.tipo != psStreamH265 {
		t.Errorf("el program stream map dice H.265 (0x24), se leyó 0x%02x", l.tipo)
	}
	if len(cuadros) != 56 {
		t.Fatalf("la muestra tiene 56 cuadros, se leyeron %d", len(cuadros))
	}
	var au h264.AnnexB
	if err := au.Unmarshal(cuadros[0].datos); err != nil {
		t.Fatal(err)
	}
	// El archivo abre con VPS, SPS, PPS e IDR_W_RADL: se puede empezar a decodificar ahí.
	var tipos []byte
	for _, n := range au {
		tipos = append(tipos, (n[0]>>1)&0x3F)
	}
	if !bytes.Equal(tipos, []byte{32, 33, 34, 19}) {
		t.Errorf("el primer cuadro debía ser VPS SPS PPS IDR, fue %v", tipos)
	}
	for i := 1; i < len(cuadros); i++ {
		if cuadros[i].pts <= cuadros[i-1].pts {
			t.Fatalf("PTS no crecientes en el cuadro %d", i)
		}
	}
}

// lectorContado cuenta lo leído, para comprobar que la descarga se corta temprano.
type lectorContado struct {
	io.Reader
	n       *int
	cerrado *bool
}

// Read entrega de a 4 KB, como llega por la red: sin eso el búfer del lector se traga la
// muestra entera de una vez y no se puede ver el corte.
func (l lectorContado) Read(p []byte) (int, error) {
	if len(p) > 4096 {
		p = p[:4096]
	}
	n, err := l.Reader.Read(p)
	*l.n += n
	return n, err
}

func (l lectorContado) Close() error { *l.cerrado = true; return nil }

func TestExtraerDescargaCortaApenasTieneLaVentana(t *testing.T) {
	muestra := muestraG1(t)
	desde := time.Date(2026, 11, 7, 9, 28, 38, 0, time.Local)
	var leidos int
	var cerrado bool
	dest := filepath.Join(t.TempDir(), "clip.mp4")

	res, err := ExtraerDescarga(context.Background(), Descarga{
		Abrir: func(context.Context, string) (io.ReadCloser, error) {
			return lectorContado{bytes.NewReader(muestra), &leidos, &cerrado}, nil
		},
		Archivos: []ArchivoGrabado{{Desde: desde, PlaybackURI: "x"}},
		// Una ventana que empieza en medio del primer GOP: el clip arranca en el keyframe
		// anterior y termina antes del segundo.
		Start:    desde.Add(time.Second),
		Duration: 2 * time.Second,
		Dest:     dest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Via != ViaDescarga || res.Codec != "H.265" {
		t.Errorf("Via=%q Codec=%q", res.Via, res.Codec)
	}
	// Se pidió desde el segundo 1: el clip arranca en el keyframe del segundo 0, y eso es
	// lo que hace exacto el offset_s del sidecar.
	if !res.Inicio.Equal(desde) {
		t.Errorf("Inicio=%v, debía ser el keyframe en %v", res.Inicio, desde)
	}
	// Del keyframe en 0 s hasta los 3 s: unos 36 cuadros a 12 fps.
	if res.Samples < 30 || res.Samples > 40 {
		t.Errorf("se esperaban ~36 muestras (0 a 3 s), hubo %d", res.Samples)
	}
	if !cerrado {
		t.Error("la descarga debía cerrarse")
	}
	if leidos >= len(muestra) {
		t.Errorf("debía cortar la lectura al pasar el final de la ventana, leyó %d de %d", leidos, len(muestra))
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("no quedó el clip: %v", err)
	}
}

// Empalme: el Desde de cada archivo viene redondeado al segundo y el primer cuadro del
// archivo nuevo puede caer antes que el último del anterior. Medido en el corte de las
// 09:28:38 de la G1: el IDR que abría el segundo archivo se descartaba y el resto del GOP
// salía gris.
func TestExtraerDescargaEmpalmaArchivosConDesdeRedondeado(t *testing.T) {
	muestra := muestraG1(t)
	d1 := time.Date(2026, 11, 7, 9, 28, 38, 0, time.Local)
	// La muestra dura ~4.6 s; el segundo archivo "empieza" a los 4 s, antes del final del
	// primero.
	d2 := d1.Add(4 * time.Second)
	dest := filepath.Join(t.TempDir(), "clip.mp4")

	res, err := ExtraerDescarga(context.Background(), Descarga{
		Abrir: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(muestra)), nil
		},
		Archivos: []ArchivoGrabado{{Desde: d1, PlaybackURI: "1"}, {Desde: d2, PlaybackURI: "2"}},
		Start:    d1,
		Duration: 8 * time.Second,
		Dest:     dest,
	})
	if err != nil {
		t.Fatalf("el empalme no debía fallar: %v", err)
	}
	// Los 56 cuadros del primero más los ~3.4 s del segundo que caben en la ventana: si el
	// IDR de la costura se perdiera, ExtraerDescarga fallaría por DTS.
	if res.Samples <= 56 {
		t.Errorf("el segundo archivo no aportó cuadros: %d muestras", res.Samples)
	}
}

func TestExtraerDescargaSinVideoEnLaVentana(t *testing.T) {
	muestra := muestraG1(t)
	desde := time.Date(2026, 11, 7, 9, 28, 38, 0, time.Local)
	_, err := ExtraerDescarga(context.Background(), Descarga{
		Abrir: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(muestra)), nil
		},
		Archivos: []ArchivoGrabado{{Desde: desde, PlaybackURI: "x"}},
		Start:    desde.Add(time.Hour),
		Duration: 2 * time.Second,
		Dest:     filepath.Join(t.TempDir(), "clip.mp4"),
	})
	if !errors.Is(err, ErrNoVideo) {
		t.Errorf("una ventana fuera del archivo debía dar ErrNoVideo, dio %v", err)
	}
}
