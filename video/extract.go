// Package video extrae un tramo grabado de una cámara Hikvision y lo escribe como MP4.
//
// Habla RTSP en Go puro, sin ffmpeg: así el binario cross-compila a cualquier
// arquitectura sin mantener un ejecutable externo por plataforma, y las credenciales
// nunca aparecen en la línea de comandos de otro proceso.
//
// Es transporte: sin actores, sin política de logs y sin reintentos. Quién extrae,
// cuándo y qué hacer con un error lo decide el actor.
//
// CUIDADO, comportamiento verificado de la cámara: pedir un instante SIN grabación no
// devuelve error, devuelve el tramo disponible más cercano. Un clip así queda con el
// nombre de un evento y muestra otro momento. Quien llame debe confirmar antes, con
// isapi.SearchRecordings, que exista cobertura para la ventana pedida.
package video

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"
	"github.com/pion/rtp"
)

// timeScale es el reloj RTP de H.264.
const timeScale = 90000

// ErrNoVideo indica que la cámara no entregó muestras utilizables.
var ErrNoVideo = errors.New("video: la cámara no entregó muestras")

// Request describe un recorte.
type Request struct {
	// Host de la cámara, "ip" o "ip:puerto". El puerto por defecto es 554.
	Host string
	User string
	Pass string
	// Track es el track de grabación, típicamente 101 para el canal 1.
	Track int
	// Start es el instante inicial del recorte, en la hora de la cámara.
	Start time.Time
	// Duration es cuánto video extraer.
	Duration time.Duration
	// Dest es el archivo MP4 final. Se escribe primero como Dest+".part" y se
	// renombra al terminar: el rename es atómico, así que un proceso que recorra
	// el directorio nunca ve un archivo a medio escribir.
	Dest string
}

// Result resume lo extraído.
type Result struct {
	Samples  int
	Duration time.Duration
	Bytes    int64
	Elapsed  time.Duration
	// Codec es el que la cámara entregó para ESTE tramo: "H.264" o "H.265". Puede diferir
	// de lo configurado hoy en la cámara, porque la SD conserva grabaciones anteriores a un
	// cambio de codec.
	Codec string
	// MaxGap es el intervalo más largo entre dos fotos consecutivas del clip.
	//
	// Es la medida de si el clip se ve fluido o congelado, y sirve para juzgarlo sin abrirlo.
	// A 20 fps lo normal es 0.05 s. Con H.264+ (SmartCodec) y escena quieta la cámara deja
	// de emitir fotos: medido, un hueco de 5.7 s en un clip de 10 segundos, que es
	// exactamente el "no se vio bien el paso de la persona".
	MaxGap time.Duration
}

// PlaybackURL arma la URL de reproducción por hora.
//
// El formato de starttime es el compacto UTC que devuelve la propia cámara en el
// playbackURI de una búsqueda ("20260818T155900Z"). El separado por guiones también
// funciona, pero se usa el compacto por ser el que emite el equipo.
func (r Request) PlaybackURL() string {
	host := r.Host
	if filepath.Ext(host) == "" && !hasPort(host) {
		host += ":554"
	}
	return fmt.Sprintf("rtsp://%s/Streaming/tracks/%d/?starttime=%s",
		host, r.Track, r.Start.UTC().Format("20060102T150405Z"))
}

func hasPort(host string) bool {
	for i := len(host) - 1; i >= 0; i-- {
		switch host[i] {
		case ':':
			return true
		case ']':
			return false
		}
	}
	return false
}

type sample struct {
	dts     int64
	pts     int64
	sync    bool
	payload []byte
}

// Extract descarga el tramo y escribe el MP4. La reproducción va a tiempo real: un
// recorte de diez segundos tarda unos diez segundos, así que ctx debe traer un plazo
// holgado y es quien corta si la cámara deja de responder.
// flujoCodec aísla lo único que cambia entre H.264 y H.265. Todo el resto del recorte
// —conexión, SETUP, PLAY, acumulación de muestras y escritura del MP4— es idéntico.
//
// Se soportan los dos porque la SD puede tener grabaciones de antes y de después de un
// cambio de codec en la cámara: el extractor toma lo que la cámara ofrezca para ese tramo,
// no lo que esté configurado ahora.
type flujoCodec struct {
	nombre string
	medi   *description.Media
	forma  format.Format
	// decode arma un access unit a partir de un paquete RTP.
	decode func(*rtp.Packet) ([][]byte, error)
	// dts devuelve el instante de decodificación del access unit.
	dts func(au [][]byte, pts int64) (int64, error)
	// sync indica si el access unit es un punto de entrada, o sea un keyframe.
	sync func(au [][]byte) bool
	// sniff se queda con los parameter sets que vengan en banda: los de la SDP pueden no
	// coincidir con los del tramo grabado.
	sniff func(au [][]byte)
	// mp4 arma el descriptor del track con los parameter sets vistos hasta ahora.
	mp4 func() codecs.Codec
}

// nuevoFlujo elige el codec según lo que la cámara ofrezca en la SDP. Devuelve nil si no
// hay ninguno de los dos.
func nuevoFlujo(desc *description.Session) (*flujoCodec, error) {
	var f264 *format.H264
	if medi := desc.FindFormat(&f264); medi != nil {
		dec, err := f264.CreateDecoder()
		if err != nil {
			return nil, fmt.Errorf("video: decoder RTP H.264: %w", err)
		}
		ex := h264.NewDTSExtractor()
		ex.Initialize()
		sps, pps := f264.SPS, f264.PPS
		return &flujoCodec{
			nombre: "H.264",
			medi:   medi,
			forma:  f264,
			decode: dec.Decode,
			dts:    ex.Extract,
			sync:   h264.IsRandomAccess,
			sniff: func(au [][]byte) {
				for _, nalu := range au {
					if len(nalu) == 0 {
						continue
					}
					// En H.264 el tipo son los 5 bits bajos del primer byte.
					switch h264.NALUType(nalu[0] & 0x1F) {
					case h264.NALUTypeSPS:
						sps = nalu
					case h264.NALUTypePPS:
						pps = nalu
					}
				}
			},
			mp4: func() codecs.Codec { return &codecs.H264{SPS: sps, PPS: pps} },
		}, nil
	}

	var f265 *format.H265
	if medi := desc.FindFormat(&f265); medi != nil {
		dec, err := f265.CreateDecoder()
		if err != nil {
			return nil, fmt.Errorf("video: decoder RTP H.265: %w", err)
		}
		ex := h265.NewDTSExtractor()
		ex.Initialize()
		vps, sps, pps := f265.VPS, f265.SPS, f265.PPS
		return &flujoCodec{
			nombre: "H.265",
			medi:   medi,
			forma:  f265,
			decode: dec.Decode,
			dts:    ex.Extract,
			sync:   h265.IsRandomAccess,
			sniff: func(au [][]byte) {
				for _, nalu := range au {
					if len(nalu) == 0 {
						continue
					}
					// H.265 corre el tipo un bit y usa 6 bits, no 5: es la trampa al
					// portar el sniffing desde H.264.
					switch h265.NALUType((nalu[0] >> 1) & 0x3F) {
					case h265.NALUType_VPS_NUT:
						vps = nalu
					case h265.NALUType_SPS_NUT:
						sps = nalu
					case h265.NALUType_PPS_NUT:
						pps = nalu
					}
				}
			},
			mp4: func() codecs.Codec { return &codecs.H265{VPS: vps, SPS: sps, PPS: pps} },
		}, nil
	}
	return nil, errors.New("video: la cámara no ofrece un stream H.264 ni H.265")
}

func Extract(ctx context.Context, req Request) (*Result, error) {
	if req.Duration <= 0 {
		return nil, fmt.Errorf("video: duración inválida %v", req.Duration)
	}
	if len(req.Dest) == 0 {
		return nil, errors.New("video: falta el destino")
	}

	inicio := time.Now()
	u, err := base.ParseURL(req.PlaybackURL())
	if err != nil {
		return nil, fmt.Errorf("video: URL: %w", err)
	}
	if len(req.User) > 0 {
		u.User = url.UserPassword(req.User, req.Pass)
	}

	// TCP explícito: por defecto gortsplib intenta UDP y pierde unos segundos en el
	// fallback, igual que el -rtsp_transport tcp de ffmpeg.
	proto := gortsplib.ProtocolTCP
	c := gortsplib.Client{Scheme: u.Scheme, Host: u.Host, Protocol: &proto}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("video: conectando: %w", err)
	}
	defer c.Close()

	desc, _, err := c.Describe(u)
	if err != nil {
		return nil, fmt.Errorf("video: DESCRIBE: %w", err)
	}
	flujo, err := nuevoFlujo(desc)
	if err != nil {
		return nil, err
	}

	var (
		samples  []sample
		firstDTS int64
		haveDTS  bool
		done     = make(chan struct{})
		finished bool
	)
	wanted := int64(req.Duration.Seconds() * timeScale)

	if _, err := c.Setup(desc.BaseURL, flujo.medi, 0, 0); err != nil {
		return nil, fmt.Errorf("video: SETUP: %w", err)
	}

	c.OnPacketRTP(flujo.medi, flujo.forma, func(pkt *rtp.Packet) {
		if finished {
			return
		}
		pts, ok := c.PacketPTS(flujo.medi, pkt)
		if !ok {
			return
		}
		au, err := flujo.decode(pkt)
		if err != nil {
			// Un access unit repartido en varios paquetes no es un error.
			return
		}
		flujo.sniff(au)
		dts, err := flujo.dts(au, int64(pts))
		if err != nil {
			return
		}
		// El empaquetado con longitud al frente es el mismo para los dos codecs: dentro
		// de un MP4, H.265 usa la misma forma que H.264 (ISO 14496-15).
		payload, err := h264.AVCC(au).Marshal()
		if err != nil {
			return
		}
		if !haveDTS {
			firstDTS, haveDTS = dts, true
		}
		samples = append(samples, sample{dts: dts, pts: int64(pts), sync: flujo.sync(au), payload: payload})
		if dts-firstDTS >= wanted {
			finished = true
			close(done)
		}
	})

	if _, err := c.Play(nil); err != nil {
		return nil, fmt.Errorf("video: PLAY: %w", err)
	}

	select {
	case <-done:
	case <-ctx.Done():
		// Se escribe lo acumulado: un recorte parcial de un evento real vale más
		// que nada, y el Result informa la duración conseguida.
	}
	c.Close()

	if len(samples) == 0 {
		return nil, ErrNoVideo
	}

	res, err := writeMP4(req.Dest, samples, flujo.mp4())
	if err != nil {
		return nil, err
	}
	res.Codec = flujo.nombre
	res.Elapsed = time.Since(inicio)
	return res, nil
}

// writeMP4 arma un MP4 progresivo y lo deja en dest mediante rename atómico.
func writeMP4(dest string, samples []sample, codec codecs.Codec) (*Result, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("video: creando directorio: %w", err)
	}

	out := make([]*pmp4.Sample, 0, len(samples))
	for i, s := range samples {
		var dur int64
		switch {
		case i+1 < len(samples):
			dur = samples[i+1].dts - s.dts
		case i > 0:
			dur = s.dts - samples[i-1].dts
		default:
			dur = timeScale / 20
		}
		if dur <= 0 {
			dur = 1
		}
		payload := s.payload
		out = append(out, &pmp4.Sample{
			Duration:        uint32(dur),
			PTSOffset:       int32(s.pts - s.dts),
			IsNonSyncSample: !s.sync,
			PayloadSize:     uint32(len(payload)),
			GetPayload:      func() ([]byte, error) { return payload, nil },
		})
	}

	pres := pmp4.Presentation{Tracks: []*pmp4.Track{{
		ID:        1,
		TimeScale: timeScale,
		Codec:     codec,
		Samples:   out,
	}}}

	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, fmt.Errorf("video: creando %s: %w", tmp, err)
	}
	if err := pres.Marshal(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, fmt.Errorf("video: escribiendo MP4: %w", err)
	}
	size, _ := f.Seek(0, 1)
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("video: cerrando %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("video: renombrando a %s: %w", dest, err)
	}

	span := samples[len(samples)-1].dts - samples[0].dts

	// El hueco más grande entre fotos consecutivas. Se mide sobre dts y no sobre pts porque
	// dts es el orden en que se decodifican, que es el que marca el avance del tiempo.
	var maxGap int64
	for i := 1; i < len(samples); i++ {
		if d := samples[i].dts - samples[i-1].dts; d > maxGap {
			maxGap = d
		}
	}

	return &Result{
		Samples:  len(samples),
		Duration: time.Duration(span) * time.Second / timeScale,
		Bytes:    size,
		MaxGap:   time.Duration(maxGap) * time.Second / timeScale,
	}, nil
}
