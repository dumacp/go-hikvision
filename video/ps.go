package video

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// Lector mínimo de MPEG-PS (ISO 13818-1), lo justo para el archivo que entrega
// /ISAPI/ContentMgmt/download.
//
// Medido en la G1 (V4.2.2): un pack header (0xBA) por cuadro, un system header (0xBB) y
// un program stream map (0xBC) por GOP, el video en 0xE0 —un PES por cuadro, varios si el
// cuadro no entra en 64 KB— con PTS en el primero, y datos privados de Hikvision en 0xBD,
// que se ignoran. mediacommon trae MPEG-TS pero no PS, y para esto no hace falta más.

// Tipos de stream del program stream map.
const (
	psStreamH264 = 0x1B
	psStreamH265 = 0x24
)

// errCortar lo devuelve quien consume para detener la lectura sin que sea un error.
var errCortar = errors.New("video: lectura cortada")

// unidadPS es un cuadro de video: los bytes Annex-B de todos los PES que comparten un PTS.
type unidadPS struct {
	pts   int64
	datos []byte
}

// lectorPS recorre un MPEG-PS y entrega los cuadros de video de a uno.
type lectorPS struct {
	r *bufio.Reader
	// tipo es el stream_type del video según el program stream map, 0 si todavía no
	// apareció.
	tipo byte
}

func nuevoLectorPS(r io.Reader) *lectorPS {
	return &lectorPS{r: bufio.NewReaderSize(r, 256<<10)}
}

// recorrer llama a emitir con cada cuadro completo, en orden. Si emitir devuelve
// errCortar, recorrer termina sin error y sin leer el resto: es lo que permite cerrar una
// descarga apenas se tiene la ventana.
func (l *lectorPS) recorrer(emitir func(unidadPS) error) error {
	var actual unidadPS
	hay := false
	vaciar := func() error {
		if !hay || len(actual.datos) == 0 {
			return nil
		}
		u := actual
		actual, hay = unidadPS{}, false
		return emitir(u)
	}

	for {
		id, err := l.siguienteCodigo()
		if err == io.EOF {
			if err := vaciar(); err != nil && err != errCortar {
				return err
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case id == 0xBA:
			if err := l.saltarPack(); err != nil {
				return finDeArchivo(err)
			}
		case id == 0xB9: // program end
			if err := vaciar(); err != nil && err != errCortar {
				return err
			}
			return nil
		case id >= 0xBB:
			cuerpo, err := l.leerConLargo()
			if err != nil {
				return finDeArchivo(err)
			}
			switch {
			case id == 0xBC:
				l.leerMapa(cuerpo)
			case id >= 0xE0 && id <= 0xEF:
				pts, datos, conPTS, err := pes(cuerpo)
				if err != nil {
					continue // un PES corrupto se salta; el cuadro siguiente resincroniza
				}
				if conPTS && hay && pts != actual.pts {
					if err := vaciar(); err != nil {
						if err == errCortar {
							return nil
						}
						return err
					}
				}
				if !hay {
					if !conPTS {
						continue // continuación de un cuadro cuyo inicio no vimos
					}
					actual, hay = unidadPS{pts: pts}, true
				}
				actual.datos = append(actual.datos, datos...)
			}
		}
	}
}

// finDeArchivo trata un archivo cortado a mitad de un paquete como un final normal: es lo
// que pasa cuando se cierra la descarga.
func finDeArchivo(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil
	}
	return err
}

// siguienteCodigo avanza hasta el próximo prefijo 00 00 01 y devuelve el byte de stream
// id que lo sigue.
func (l *lectorPS) siguienteCodigo() (byte, error) {
	ceros := 0
	for {
		b, err := l.r.ReadByte()
		if err != nil {
			return 0, err
		}
		switch {
		case b == 0:
			ceros++
		case b == 1 && ceros >= 2:
			return l.r.ReadByte()
		default:
			ceros = 0
		}
	}
}

// saltarPack descarta el resto de un pack header. MPEG-2 trae 10 bytes fijos y un relleno
// cuyo largo va en los 3 bits bajos del último; MPEG-1, 8 fijos.
func (l *lectorPS) saltarPack() error {
	b, err := l.r.Peek(1)
	if err != nil {
		return err
	}
	if b[0]>>6 == 1 { // MPEG-2
		h := make([]byte, 10)
		if _, err := io.ReadFull(l.r, h); err != nil {
			return err
		}
		_, err = l.r.Discard(int(h[9] & 0x07))
		return err
	}
	_, err = l.r.Discard(8)
	return err
}

func (l *lectorPS) leerConLargo() ([]byte, error) {
	var n [2]byte
	if _, err := io.ReadFull(l.r, n[:]); err != nil {
		return nil, err
	}
	cuerpo := make([]byte, int(n[0])<<8|int(n[1]))
	if _, err := io.ReadFull(l.r, cuerpo); err != nil {
		return nil, err
	}
	return cuerpo, nil
}

// leerMapa toma del program stream map el tipo del primer stream de video.
func (l *lectorPS) leerMapa(m []byte) {
	if len(m) < 4 {
		return
	}
	i := 4 + (int(m[2])<<8 | int(m[3])) // salta program_stream_info
	if i+2 > len(m) {
		return
	}
	fin := i + 2 + (int(m[i])<<8 | int(m[i+1]))
	i += 2
	for i+4 <= len(m) && i+4 <= fin {
		tipo, id := m[i], m[i+1]
		largo := int(m[i+2])<<8 | int(m[i+3])
		if id >= 0xE0 && id <= 0xEF {
			l.tipo = tipo
			return
		}
		i += 4 + largo
	}
}

// pes separa la cabecera de un PES de video (formato MPEG-2) de sus datos.
func pes(c []byte) (pts int64, datos []byte, conPTS bool, err error) {
	if len(c) < 3 || c[0]>>6 != 2 {
		return 0, nil, false, fmt.Errorf("video: PES sin cabecera MPEG-2")
	}
	inicio := 3 + int(c[2])
	if inicio > len(c) {
		return 0, nil, false, fmt.Errorf("video: PES con cabecera más larga que el paquete")
	}
	if c[1]&0x80 != 0 && len(c) >= 8 {
		p := c[3:8]
		pts = int64(p[0]>>1&0x07)<<30 | int64(p[1])<<22 | int64(p[2]>>1)<<15 |
			int64(p[3])<<7 | int64(p[4]>>1)
		conPTS = true
	}
	return pts, c[inicio:], conPTS, nil
}
