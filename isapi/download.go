package isapi

import (
	"bufio"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
)

// ErrDescargaNoSoportada indica que la cámara no ofrece /ISAPI/ContentMgmt/download.
//
// Es una propiedad del modelo, no un fallo pasajero: la G0 responde statusCode 4 /
// methodNotAllowed, y quien llama debería recordarlo por cámara y usar el RTSP.
var ErrDescargaNoSoportada = errors.New("isapi: la cámara no soporta la descarga de grabaciones")

type downloadRequest struct {
	XMLName     xml.Name `xml:"downloadRequest"`
	Version     string   `xml:"version,attr"`
	Xmlns       string   `xml:"xmlns,attr"`
	PlaybackURI string   `xml:"playbackURI"`
}

// Download pide un archivo grabado por HTTP y devuelve su contenido en streaming.
//
// playbackURI tiene que ser el que devolvió la búsqueda, TAL CUAL —con name y size—:
// medido en la G1 (V4.2.2), uno armado a mano con solo starttime/endtime deja la
// conexión colgada sin entregar un byte. El contenido es el archivo entero de ese tramo
// (en la G1, hasta 5 min) en MPEG-PS. No hay plazo total: la descarga se acota con ctx,
// y quien solo necesita una parte puede cerrar el cuerpo apenas la tenga.
//
// Por qué existe: en la G1 la reproducción por RTSP devuelve un stream truncado e
// idéntico para cualquier instante pedido, mientras esta descarga entrega la grabación
// intacta.
func (c *Client) Download(ctx context.Context, playbackURI string) (io.ReadCloser, error) {
	const path = "/ISAPI/ContentMgmt/download"
	// Versión y namespace exactos de la petición verificada contra la G1.
	body, err := xml.Marshal(downloadRequest{Version: "1.0", Xmlns: "http://www.isapi.org/ver20/XMLSchema", PlaybackURI: playbackURI})
	if err != nil {
		return nil, fmt.Errorf("isapi POST %s: building request: %w", path, err)
	}
	// Sin Timeout total: la descarga puede durar más que cualquier plazo fijo razonable
	// para una llamada de configuración. El plazo lo pone ctx.
	hc := &http.Client{Transport: c.hc.Transport}
	resp, err := c.abrir(ctx, hc, http.MethodPost, path, append([]byte(xml.Header), body...))
	if err != nil {
		return nil, err
	}

	// Un error llega como ResponseStatus en XML; el video, como MPEG-PS. Se mira el
	// principio del cuerpo sin consumirlo.
	br := bufio.NewReader(resp.Body)
	inicio, _ := br.Peek(512)
	esXML := strings.Contains(resp.Header.Get("Content-Type"), "xml") ||
		strings.HasPrefix(strings.TrimSpace(string(inicio)), "<")
	if resp.StatusCode < 200 || resp.StatusCode > 299 || esXML {
		data, _ := ioutil.ReadAll(io.LimitReader(br, maxBody))
		resp.Body.Close()
		if status, ok := parseResponseStatus(data); ok && !status.OK() {
			if status.StatusCode == 4 || strings.EqualFold(status.SubStatusCode, "methodNotAllowed") ||
				strings.EqualFold(status.SubStatusCode, "notSupport") {
				return nil, fmt.Errorf("isapi POST %s: %w (%v)", path, ErrDescargaNoSoportada, status)
			}
			return nil, fmt.Errorf("isapi POST %s: %w", path, status)
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			return nil, fmt.Errorf("isapi POST %s: %w (HTTP %d)", path, ErrDescargaNoSoportada, resp.StatusCode)
		}
		return nil, fmt.Errorf("isapi POST %s: HTTP %d, respuesta que no es video", path, resp.StatusCode)
	}
	return struct {
		io.Reader
		io.Closer
	}{br, resp.Body}, nil
}
