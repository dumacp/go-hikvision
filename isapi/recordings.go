package isapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"time"
)

// searchTimeLayout es el formato que la cámara acepta y devuelve en las búsquedas de
// grabaciones: ISO 8601 en UTC con Z.
const searchTimeLayout = "2006-01-02T15:04:05Z"

// CMSearchDescription es la petición de búsqueda de grabaciones.
type CMSearchDescription struct {
	XMLName xml.Name `xml:"CMSearchDescription"`
	Version string   `xml:"version,attr,omitempty"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`

	SearchID    string `xml:"searchID"`
	TrackIDList []int  `xml:"trackIDList>trackID"`
	TimeSpans   []struct {
		StartTime string `xml:"startTime"`
		EndTime   string `xml:"endTime"`
	} `xml:"timeSpanList>timeSpan"`
	MaxResults int `xml:"maxResults"`
	// Sí, "Postion": el nombre del nodo viene mal escrito en la propia API.
	SearchResultPosition int      `xml:"searchResultPostion"`
	MetadataDescriptors  []string `xml:"metadataList>metadataDescriptor"`
}

// Recording es un tramo grabado disponible en la cámara.
type Recording struct {
	TrackID     int    `xml:"trackID"`
	StartTime   string `xml:"timeSpan>startTime"`
	EndTime     string `xml:"timeSpan>endTime"`
	ContentType string `xml:"mediaSegmentDescriptor>contentType"`
	CodecType   string `xml:"mediaSegmentDescriptor>codecType"`
	PlaybackURI string `xml:"mediaSegmentDescriptor>playbackURI"`
}

// Span devuelve el intervalo cubierto por el tramo.
func (r Recording) Span() (start, end time.Time, err error) {
	if start, err = tolerantTime(r.StartTime); err != nil {
		return
	}
	end, err = tolerantTime(r.EndTime)
	return
}

// Covers indica si el tramo contiene por completo la ventana pedida.
func (r Recording) Covers(from, to time.Time) bool {
	return r.covers(from, to, false)
}

// covers es Covers sabiendo con qué convención respondió la cámara.
//
// Con horaLocal, un instante que la cámara devuelve con `Z` es en realidad su reloj de
// pared: se lo reinterpreta en la zona de la ventana pedida antes de comparar. Leerlo
// como UTC lo correría el desfase completo (5 h en Colombia) y ningún tramo cubriría
// nada. Un instante con desfase explícito (`-05:00`) no se toca: ese sí dice lo que es.
func (r Recording) covers(from, to time.Time, horaLocal bool) bool {
	start, end, err := r.Span()
	if err != nil {
		return false
	}
	if horaLocal {
		start = relojDePared(r.StartTime, start, from.Location())
		end = relojDePared(r.EndTime, end, from.Location())
	}
	return !from.Before(start) && !to.After(end)
}

// relojDePared reinterpreta en loc un instante que la cámara marcó con `Z` pero que en
// realidad es su hora local. Si el texto no termina en `Z` lo devuelve como estaba.
func relojDePared(raw string, t time.Time, loc *time.Location) time.Time {
	if !strings.HasSuffix(strings.TrimSpace(raw), "Z") {
		return t
	}
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(),
		u.Nanosecond(), loc)
}

// CMSearchResult es la respuesta de la búsqueda.
type CMSearchResult struct {
	XMLName            xml.Name    `xml:"CMSearchResult"`
	SearchID           string      `xml:"searchID"`
	ResponseStatus     bool        `xml:"responseStatus"`
	ResponseStatusStrg string      `xml:"responseStatusStrg"`
	NumOfMatches       int         `xml:"numOfMatches"`
	Matches            []Recording `xml:"matchList>searchMatchItem"`
}

// SearchRecordings pregunta a la cámara qué tramos tiene grabados en una ventana.
//
// Es la guarda obligatoria antes de extraer video: verificado en un DS-2XM6825G0, pedir
// un instante sin grabación NO devuelve error, devuelve el tramo disponible más cercano.
// Sin este chequeo un recorte queda archivado con el nombre de un evento y muestra otro
// momento.
//
// searchID identifica la búsqueda para que la cámara pueda paginar; con una ventana
// corta y un maxResults holgado no hace falta paginar.
func (c *Client) SearchRecordings(ctx context.Context, track int, from, to time.Time, searchID string, maxResults int) (*CMSearchResult, error) {
	return c.buscar(ctx, track, from, to, searchID, maxResults, false)
}

// buscar es SearchRecordings eligiendo la convención de hora.
//
// El `Z` del formato NO garantiza UTC: medido contra la DS-2XM6825G1 con firmware
// V4.2.2, la búsqueda lee el instante como su reloj de pared. Para la ventana de hace
// 5 a 2 minutos, pedida en UTC respondió "NO MATCHES"; la misma ventana con la hora
// local respondió OK con el tramo que la cubría. Con horaLocal se manda el reloj de
// pared de from/to —en su propia zona— con el mismo sufijo, igual que
// video.Request.PlaybackURL.
func (c *Client) buscar(ctx context.Context, track int, from, to time.Time, searchID string, maxResults int, horaLocal bool) (*CMSearchResult, error) {
	if to.Before(from) {
		return nil, fmt.Errorf("isapi: ventana invertida, %v a %v", from, to)
	}
	if maxResults <= 0 {
		maxResults = 20
	}
	req := &CMSearchDescription{
		Version:              Version,
		Xmlns:                NamespaceHikvision,
		SearchID:             searchID,
		TrackIDList:          []int{track},
		MaxResults:           maxResults,
		SearchResultPosition: 0,
		MetadataDescriptors:  []string{"//recordType.meta.std-cgi.com"},
	}
	req.TimeSpans = append(req.TimeSpans, struct {
		StartTime string `xml:"startTime"`
		EndTime   string `xml:"endTime"`
	}{
		StartTime: formatoBusqueda(from, horaLocal),
		EndTime:   formatoBusqueda(to, horaLocal),
	})

	out := new(CMSearchResult)
	if err := c.post(ctx, "/ISAPI/ContentMgmt/search", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// formatoBusqueda escribe un instante para la búsqueda: en UTC, o con el reloj de pared
// de su propia zona si horaLocal. El sufijo `Z` va en los dos casos porque es lo único
// que la cámara acepta.
func formatoBusqueda(t time.Time, horaLocal bool) string {
	if horaLocal {
		return t.Format(searchTimeLayout)
	}
	return t.UTC().Format(searchTimeLayout)
}

// tramoHuecoMax es el hueco más grande entre dos tramos consecutivos que todavía se
// considera grabación continua.
//
// La G1 (V4.2.2) parte la grabación en archivos de 5 minutos y la búsqueda devuelve un
// tramo por archivo, pegados (medido: 09:11:21..09:16:21 y 09:16:21..). Sin unirlos,
// toda ventana que cruce un corte quedaba "sin grabación" aunque cada segundo estuviera
// grabado — con 16 s de ventana, uno de cada ~19 pasos. Dos segundos absorben el
// redondeo al segundo de los extremos sin tapar un corte real de la grabación.
const tramoHuecoMax = 2 * time.Second

// BusquedaAtras es cuánto antes de la ventana se extiende la búsqueda.
//
// La cámara solo devuelve tramos que tocan lo que se le pregunta, y su índice puede ir
// atrasado respecto de lo que ya grabó (medido en la G1: hasta ~2 min, actualizándose de
// a saltos). Buscando solo la ventana, un índice que todavía termina antes de ella
// responde "NO MATCHES" y no se distingue de una cámara que no graba. Mirando hacia
// atrás se ve el tramo que viene grabando y que todavía no llegó, y eso es Pendiente.
const BusquedaAtras = 3 * time.Minute

// Cobertura es lo que la cámara tiene grabado para una ventana.
type Cobertura struct {
	// Tramo es la grabación continua que contiene toda la ventana, o nil. Si la
	// cámara la partió en varios archivos, es la unión de ellos.
	Tramo *Recording
	// HoraLocal es la convención de hora con la que la cámara respondió.
	HoraLocal bool
	// Pendiente indica que la cámara viene grabando desde antes de la ventana pero su
	// índice todavía no llega a su final: lo más probable es que el tramo aparezca si
	// se pregunta de nuevo en un rato. Solo tiene sentido con Tramo en nil.
	Pendiente bool
	// IndexadoHasta es el último instante del tramo que motivó Pendiente.
	IndexadoHasta time.Time
}

// HasCoverage indica qué tiene grabado la cámara para la ventana [from, to].
//
// Un tramo que solo cubre la ventana en parte no sirve —el recorte saldría incompleto o
// desplazado—, pero si la parte que falta es el final y la cámara venía grabando, se
// informa Pendiente en vez de un "no hay" definitivo: ver BusquedaAtras.
//
// Prueba las dos convenciones de hora, empezando por la preferida (preferLocal), y
// devuelve en HoraLocal la que obtuvo respuesta útil, para que quien llama la guarde por
// cámara y la pase la próxima vez. Con preferLocal en false el primer intento es la
// búsqueda en UTC de siempre, así que una cámara que ya funcionaba no cambia en nada.
//
// Sin esto la G1 (V4.2.2) nunca tenía cobertura: la búsqueda en UTC le pedía un tramo
// 5 h en el futuro, cada evento terminaba en "sin grabación" y el reintento en hora
// local que ya hace video.Extract no llegaba a ejecutarse, porque esta guarda corre
// antes.
//
// Solo devuelve error cuando NINGUNA de las dos búsquedas pudo responder, y entonces es
// el de la primera. Una búsqueda que respondió sin tramo es concluyente: "no hay".
func (c *Client) HasCoverage(ctx context.Context, track int, from, to time.Time, searchID string, preferLocal bool) (Cobertura, error) {
	cov, err := c.cobertura(ctx, track, from, to, searchID, preferLocal)
	if err == nil && (cov.Tramo != nil || cov.Pendiente) {
		return cov, nil
	}
	// Con el equipo en UTC las dos formas son idénticas: repetir sería pagar dos veces
	// la misma búsqueda.
	if _, desfase := from.Zone(); desfase == 0 {
		return Cobertura{HoraLocal: preferLocal}, err
	}
	cov2, err2 := c.cobertura(ctx, track, from, to, searchID, !preferLocal)
	if err2 == nil && (cov2.Tramo != nil || cov2.Pendiente) {
		return cov2, nil
	}
	if err == nil || err2 == nil {
		return Cobertura{HoraLocal: preferLocal}, nil
	}
	return Cobertura{HoraLocal: preferLocal}, err
}

// cobertura hace UNA búsqueda con la convención dada.
func (c *Client) cobertura(ctx context.Context, track int, from, to time.Time, searchID string, horaLocal bool) (Cobertura, error) {
	cov := Cobertura{HoraLocal: horaLocal}
	res, err := c.buscar(ctx, track, from.Add(-BusquedaAtras), to, searchID, 20, horaLocal)
	if err != nil {
		return cov, err
	}
	for _, t := range unirTramos(res.Matches, from.Location(), horaLocal) {
		switch {
		case !from.Before(t.desde) && !to.After(t.hasta):
			rec := t.rec
			cov.Tramo = &rec
			cov.Pendiente = false
			return cov, nil
		case !from.Before(t.desde) && to.After(t.hasta):
			// Viene grabando desde antes de la ventana y el índice no llega al final.
			cov.Pendiente = true
			if t.hasta.After(cov.IndexadoHasta) {
				cov.IndexadoHasta = t.hasta
			}
		}
	}
	return cov, nil
}

// tramo es un intervalo grabado ya interpretado, con el Recording que lo representa.
type tramo struct {
	desde, hasta time.Time
	rec          Recording
}

// unirTramos interpreta los tramos de la respuesta, los ordena y une los contiguos
// (ver tramoHuecoMax). El Recording de un tramo unido conserva los datos del primero
// —codec, URI— y el final del último.
func unirTramos(matches []Recording, loc *time.Location, horaLocal bool) []tramo {
	var ts []tramo
	for _, m := range matches {
		d, h, err := m.Span()
		if err != nil {
			continue
		}
		if horaLocal {
			d = relojDePared(m.StartTime, d, loc)
			h = relojDePared(m.EndTime, h, loc)
		}
		ts = append(ts, tramo{desde: d, hasta: h, rec: m})
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].desde.Before(ts[j].desde) })
	var out []tramo
	for _, t := range ts {
		if n := len(out); n > 0 && !t.desde.After(out[n-1].hasta.Add(tramoHuecoMax)) {
			if t.hasta.After(out[n-1].hasta) {
				out[n-1].hasta = t.hasta
				out[n-1].rec.EndTime = t.rec.EndTime
			}
			continue
		}
		out = append(out, t)
	}
	return out
}
