package video

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
)

// ViaRTSP y ViaDescarga dicen por dónde se obtuvo un clip (Result.Via).
const (
	ViaRTSP     = "rtsp"
	ViaDescarga = "descarga"
)

// ArchivoGrabado es un archivo de la SD de la cámara, tal como lo describe la búsqueda.
type ArchivoGrabado struct {
	// Desde es el instante de su primer cuadro, en la hora de la cámara.
	Desde time.Time
	// PlaybackURI es el que devolvió la búsqueda, sin tocar.
	PlaybackURI string
}

// Descarga describe un recorte obtenido bajando los archivos grabados por HTTP.
type Descarga struct {
	// Abrir entrega el contenido de un archivo (isapi.Client.Download). Va como función
	// para que este paquete siga sin saber de ISAPI y para poder probarlo sin cámara.
	Abrir func(ctx context.Context, playbackURI string) (io.ReadCloser, error)
	// Archivos son los que cubren la ventana, en orden: si la ventana cruza el corte
	// entre dos archivos se baja el segundo a continuación.
	Archivos []ArchivoGrabado
	// Start, Duration y Dest significan lo mismo que en Request.
	Start    time.Time
	Duration time.Duration
	Dest     string
}

// cuadroPS es un cuadro ya ubicado en el tiempo.
type cuadroPS struct {
	t    time.Time
	au   [][]byte
	sync bool
}

// ExtraerDescarga recorta [Start, Start+Duration] leyendo los archivos grabados en orden y
// escribe el MP4.
//
// Es la vía para la G1 (V4.2.2), cuya reproducción por RTSP devuelve un stream truncado:
// la descarga entrega el archivo intacto. El archivo se LEE desde el principio —la cámara
// no acepta pedir solo una parte—, pero no se guarda: se descartan los cuadros anteriores
// a la ventana salvo el GOP que la abre, y la conexión se cierra apenas se pasa del final.
// En el peor caso, una ventana al final de un archivo de 5 min, se leen ~74 MB por la red
// local (medido: ~247 KB/s grabados).
//
// El instante de cada cuadro sale del Desde de su archivo más el avance de su PTS. Como en
// el RTSP, el clip arranca en el keyframe anterior a Start, así que trae un poco más de
// pre-roll que el pedido, nunca menos.
func ExtraerDescarga(ctx context.Context, d Descarga) (*Result, error) {
	if d.Duration <= 0 {
		return nil, fmt.Errorf("video: duración inválida %v", d.Duration)
	}
	if len(d.Dest) == 0 {
		return nil, errors.New("video: falta el destino")
	}
	if len(d.Archivos) == 0 {
		return nil, errors.New("video: no hay archivos que bajar")
	}
	if d.Abrir == nil {
		return nil, errors.New("video: falta la función de descarga")
	}

	inicio := time.Now()
	fin := d.Start.Add(d.Duration)
	var (
		flujo     *flujoCodec
		previos   []cuadroPS // el GOP en curso antes de Start
		dentro    []cuadroPS
		terminado bool
		// ultimo y paso son el instante del último cuadro visto y el intervalo entre los
		// dos últimos, para empalmar archivos (ver abajo).
		ultimo time.Time
		paso   = time.Second / 12
	)

	for _, a := range d.Archivos {
		if terminado {
			break
		}
		rc, err := d.Abrir(ctx, a.PlaybackURI)
		if err != nil {
			if len(dentro) == 0 {
				return nil, err
			}
			break // un corte a mitad deja un clip parcial, igual que en el RTSP
		}
		l := nuevoLectorPS(rc)
		var primerPTS int64
		hayPTS := false
		desde := a.Desde
		err = l.recorrer(func(u unidadPS) error {
			if !hayPTS {
				primerPTS, hayPTS = u.pts, true
				// El Desde de cada archivo viene redondeado al segundo, así que el primer
				// cuadro de un archivo puede caer ANTES que el último del anterior. Medido
				// en el corte de las 09:28:38: el IDR que abre el segundo archivo quedaba
				// detrás, el extractor de DTS lo rechazaba y el resto del GOP salía gris.
				// Se lo corre justo detrás del último cuadro visto.
				if !ultimo.IsZero() && !desde.After(ultimo) {
					desde = ultimo.Add(paso)
				}
			}
			// El PTS es de 33 bits y puede dar la vuelta dentro de un archivo.
			avance := (u.pts - primerPTS) & (1<<33 - 1)
			t := desde.Add(time.Duration(avance) * time.Second / timeScale)
			if !ultimo.IsZero() && t.After(ultimo) {
				paso = t.Sub(ultimo)
			}
			ultimo = t

			var au h264.AnnexB
			if err := au.Unmarshal(u.datos); err != nil || len(au) == 0 {
				return nil
			}
			if flujo == nil {
				if flujo = flujoPorTipo(l.tipo, au); flujo == nil {
					return nil // todavía no se sabe el codec; espera un cuadro que lo diga
				}
			}
			flujo.sniff(au)
			c := cuadroPS{t: t, au: au, sync: flujo.sync(au)}

			if t.Before(d.Start) {
				if c.sync {
					previos = previos[:0]
				}
				if c.sync || len(previos) > 0 {
					previos = append(previos, c)
				}
				return nil
			}
			if !t.Before(fin) {
				terminado = true
				return errCortar
			}
			if len(dentro) == 0 {
				dentro, previos = append(dentro, previos...), nil
			}
			if len(dentro) == 0 && !c.sync {
				return nil // sin un keyframe todavía no se puede empezar
			}
			dentro = append(dentro, c)
			return nil
		})
		rc.Close()
		if err != nil {
			if len(dentro) == 0 {
				return nil, fmt.Errorf("video: leyendo la descarga: %w", err)
			}
			break
		}
	}

	if len(dentro) == 0 || flujo == nil {
		return nil, ErrNoVideo
	}

	base := dentro[0].t
	samples := make([]sample, 0, len(dentro))
	for _, c := range dentro {
		pts := int64(c.t.Sub(base)) * timeScale / int64(time.Second)
		dts, err := flujo.dts(c.au, pts)
		if err != nil {
			// Un cuadro descartado acá rompe la decodificación de todo lo que dependa de
			// él, y el clip sale gris sin que nada falle: mejor no escribir un clip roto.
			return nil, fmt.Errorf("video: cuadro de %s sin DTS válido: %w", c.t.Format("15:04:05.000"), err)
		}
		payload, err := h264.AVCC(c.au).Marshal()
		if err != nil {
			continue
		}
		samples = append(samples, sample{dts: dts, pts: pts, sync: c.sync, payload: payload})
	}
	if len(samples) == 0 {
		return nil, ErrNoVideo
	}

	res, err := writeMP4(d.Dest, samples, flujo.mp4())
	if err != nil {
		return nil, err
	}
	res.Codec = flujo.nombre
	res.Elapsed = time.Since(inicio)
	res.Via = ViaDescarga
	res.Inicio = base
	return res, nil
}

// flujoPorTipo elige el codec por el program stream map y, si no vino, por los NAL del
// cuadro. Devuelve nil si todavía no se puede saber.
func flujoPorTipo(tipo byte, au [][]byte) *flujoCodec {
	switch tipo {
	case psStreamH265:
		return flujoH265(nil, nil, nil)
	case psStreamH264:
		return flujoH264(nil, nil)
	}
	for _, n := range au {
		if len(n) == 0 {
			continue
		}
		switch h265.NALUType((n[0] >> 1) & 0x3F) {
		case h265.NALUType_VPS_NUT, h265.NALUType_SPS_NUT, h265.NALUType_PPS_NUT:
			return flujoH265(nil, nil, nil)
		}
		switch h264.NALUType(n[0] & 0x1F) {
		case h264.NALUTypeSPS, h264.NALUTypePPS:
			return flujoH264(nil, nil)
		}
	}
	return nil
}
