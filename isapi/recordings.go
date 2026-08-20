package isapi

import (
	"context"
	"encoding/xml"
	"fmt"
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
	start, end, err := r.Span()
	if err != nil {
		return false
	}
	return !from.Before(start) && !to.After(end)
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
		StartTime: from.UTC().Format(searchTimeLayout),
		EndTime:   to.UTC().Format(searchTimeLayout),
	})

	out := new(CMSearchResult)
	if err := c.post(ctx, "/ISAPI/ContentMgmt/search", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// HasCoverage indica si la cámara tiene grabación continua para toda la ventana.
//
// Devuelve el tramo que la cubre, o nil si ninguno lo hace. Un tramo que solo la cubre
// en parte no sirve: el recorte saldría incompleto o desplazado.
func (c *Client) HasCoverage(ctx context.Context, track int, from, to time.Time, searchID string) (*Recording, error) {
	res, err := c.SearchRecordings(ctx, track, from, to, searchID, 20)
	if err != nil {
		return nil, err
	}
	for i := range res.Matches {
		if res.Matches[i].Covers(from, to) {
			return &res.Matches[i], nil
		}
	}
	return nil, nil
}
