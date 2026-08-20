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
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
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
	var forma *format.H264
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return nil, errors.New("video: la cámara no ofrece un stream H264")
	}
	dec, err := forma.CreateDecoder()
	if err != nil {
		return nil, fmt.Errorf("video: decoder RTP: %w", err)
	}
	dtsEx := h264.NewDTSExtractor()
	dtsEx.Initialize()

	var (
		samples  []sample
		firstDTS int64
		haveDTS  bool
		sps      = forma.SPS
		pps      = forma.PPS
		done     = make(chan struct{})
		finished bool
	)
	wanted := int64(req.Duration.Seconds() * timeScale)

	if _, err := c.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return nil, fmt.Errorf("video: SETUP: %w", err)
	}

	c.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		if finished {
			return
		}
		pts, ok := c.PacketPTS(medi, pkt)
		if !ok {
			return
		}
		au, err := dec.Decode(pkt)
		if err != nil {
			// Un access unit repartido en varios paquetes no es un error.
			return
		}
		// SPS/PPS en banda: quedarse con el último visto, porque el de la SDP puede
		// no coincidir con el del tramo grabado.
		for _, nalu := range au {
			if len(nalu) == 0 {
				continue
			}
			switch h264.NALUType(nalu[0] & 0x1F) {
			case h264.NALUTypeSPS:
				sps = nalu
			case h264.NALUTypePPS:
				pps = nalu
			}
		}
		dts, err := dtsEx.Extract(au, int64(pts))
		if err != nil {
			return
		}
		payload, err := h264.AVCC(au).Marshal()
		if err != nil {
			return
		}
		if !haveDTS {
			firstDTS, haveDTS = dts, true
		}
		samples = append(samples, sample{dts: dts, pts: int64(pts), sync: h264.IsRandomAccess(au), payload: payload})
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

	res, err := writeMP4(req.Dest, samples, sps, pps)
	if err != nil {
		return nil, err
	}
	res.Elapsed = time.Since(inicio)
	return res, nil
}

// writeMP4 arma un MP4 progresivo y lo deja en dest mediante rename atómico.
func writeMP4(dest string, samples []sample, sps, pps []byte) (*Result, error) {
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
		Codec:     &codecs.H264{SPS: sps, PPS: pps},
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
	return &Result{
		Samples:  len(samples),
		Duration: time.Duration(span) * time.Second / timeScale,
		Bytes:    size,
	}, nil
}
