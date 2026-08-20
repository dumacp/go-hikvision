package isapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
)

// HDD es un medio de almacenamiento de la cámara.
type HDD struct {
	ID   int    `xml:"id"`
	Name string `xml:"hddName"`
	Type string `xml:"hddType"`
	// Status es "ok", "unformatted", "error", "idle"… Una cámara con la SD sin formatear
	// no graba nada, así que ningún clip se puede extraer: es la condición que hay que
	// detectar antes de culpar al extractor.
	Status string `xml:"status"`
	// Capacity y FreeSpace vienen en MB.
	Capacity  int `xml:"capacity"`
	FreeSpace int `xml:"freeSpace"`
}

// Healthy indica si el medio sirve para grabar.
func (h HDD) Healthy() bool { return strings.EqualFold(h.Status, "ok") }

// StorageStatus es la respuesta de /ISAPI/ContentMgmt/Storage.
type StorageStatus struct {
	XMLName xml.Name `xml:"storage"`
	HDDs    []HDD    `xml:"hddList>hdd"`
}

// GetStorage lee el estado del almacenamiento.
func (c *Client) GetStorage(ctx context.Context) (*StorageStatus, error) {
	out := new(StorageStatus)
	if err := c.get(ctx, "/ISAPI/ContentMgmt/Storage", out); err != nil {
		return nil, err
	}
	return out, nil
}

// TrackSchedule y sus tipos modelan lo justo del track de grabación para poder leer y
// reescribir el horario sin perder el resto de la configuración.
//
// La cámara devuelve un documento grande (regiones, calibración, overlays); reescribirlo
// completo con solo los campos que nos interesan borraría lo demás. Por eso el actor
// trabaja sobre el XML crudo y solo sustituye las horas: menos elegante que un struct
// completo, pero no destruye lo que no entiende.
type Track struct {
	XMLName xml.Name `xml:"Track"`
	ID      int      `xml:"id"`
	Enable  bool     `xml:"Enable"`
	// Schedule son las ventanas por día, en el orden en que vienen.
	Schedule []ScheduleAction `xml:"TrackSchedule>ScheduleBlock>ScheduleAction"`
}

// ScheduleAction es una ventana de grabación de un día.
type ScheduleAction struct {
	StartDay  string `xml:"ScheduleActionStartTime>DayOfWeek"`
	StartTime string `xml:"ScheduleActionStartTime>TimeOfDay"`
	EndDay    string `xml:"ScheduleActionEndTime>DayOfWeek"`
	EndTime   string `xml:"ScheduleActionEndTime>TimeOfDay"`
	Record    bool   `xml:"Actions>Record"`
}

// GetTrack lee la configuración de un track de grabación.
func (c *Client) GetTrack(ctx context.Context, track int) (*Track, error) {
	out := new(Track)
	if err := c.get(ctx, fmt.Sprintf("/ISAPI/ContentMgmt/record/tracks/%d", track), out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetTrackRaw devuelve el XML crudo del track, para poder reescribirlo sin perder campos.
func (c *Client) GetTrackRaw(ctx context.Context, track int) ([]byte, error) {
	return c.do(ctx, "GET", fmt.Sprintf("/ISAPI/ContentMgmt/record/tracks/%d", track), nil)
}

// PutTrackRaw escribe el XML del track tal cual.
//
// Verificado en un DS-2XM6825G0: este cambio se aplica **en caliente**, responde
// statusCode 1 y al leerlo de vuelta ya está activo. No confundir con
// /ISAPI/Streaming/channels/<ID>, que responde statusCode 7 y exige reiniciar la cámara.
func (c *Client) PutTrackRaw(ctx context.Context, track int, body []byte) error {
	_, err := c.do(ctx, "PUT", fmt.Sprintf("/ISAPI/ContentMgmt/record/tracks/%d", track), body)
	return err
}

// SetScheduleWindow reescribe las horas de todas las ventanas del XML crudo del track,
// dejando el resto del documento intacto.
//
// Devuelve el nuevo XML y cuántas ventanas cambió. Si no cambió ninguna, el llamador no
// debe escribir: es lo que hace idempotente al ciclo.
//
// Ojo: la cámara redondea al minuto. Pedir "23:59:59" queda guardado como "23:59:00", así
// que comparar contra lo pedido daría diferencia siempre y escribiría en cada ciclo. Por
// eso el minuto es la unidad de comparación.
func SetScheduleWindow(raw []byte, start, end string) ([]byte, int, error) {
	if !validTimeOfDay(start) || !validTimeOfDay(end) {
		return nil, 0, fmt.Errorf("horario inválido %q -> %q, se espera HH:MM:SS", start, end)
	}
	s := string(raw)
	var cambios int

	// Las ventanas vienen como pares StartTime/EndTime dentro de cada ScheduleAction, en
	// ese orden. Se recorre el documento sustituyendo alternadamente.
	var out strings.Builder
	resto := s
	esperaInicio := true
	for {
		abre := strings.Index(resto, "<TimeOfDay>")
		if abre < 0 {
			out.WriteString(resto)
			break
		}
		cierra := strings.Index(resto[abre:], "</TimeOfDay>")
		if cierra < 0 {
			out.WriteString(resto)
			break
		}
		cierra += abre
		actual := resto[abre+len("<TimeOfDay>") : cierra]
		quiero := end
		if esperaInicio {
			quiero = start
		}
		esperaInicio = !esperaInicio

		out.WriteString(resto[:abre+len("<TimeOfDay>")])
		if sameMinute(actual, quiero) {
			out.WriteString(actual)
		} else {
			out.WriteString(quiero)
			cambios++
		}
		resto = resto[cierra:]
	}
	return []byte(out.String()), cambios, nil
}

// sameMinute compara dos HH:MM:SS ignorando los segundos, porque la cámara los redondea.
func sameMinute(a, b string) bool {
	if len(a) < 5 || len(b) < 5 {
		return a == b
	}
	return a[:5] == b[:5]
}

func validTimeOfDay(v string) bool {
	if len(v) != 8 || v[2] != ':' || v[5] != ':' {
		return false
	}
	for i, c := range v {
		if i == 2 || i == 5 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
