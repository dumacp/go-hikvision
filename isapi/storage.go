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

// StatusNoOverwrite es el valor que el actor pone en el campo `storage` del evento cuando
// la cámara tiene la sobrescritura cíclica apagada.
//
// Es un VALOR nuevo en un campo que ya era de texto libre —la cámara devuelve ahí "ok",
// "unformatted", "error", "idle"—, no una clave nueva en el JSON. Esa distinción es
// deliberada: la forma del mensaje no cambia, así que un consumidor que trate `storage`
// como texto no se entera de nada.
const StatusNoOverwrite = "nooverwrite"

// Primero devuelve el medio que conviene reportar: el primero que NO esté sano, o el
// primero a secas si todos lo están.
//
// Se miran todos y no solo HDDs[0] porque la cámara verificada devuelve la lista con
// size="8": hoy trae un medio, pero con dos el segundo quedaba invisible y una SD rota
// podía pasar inadvertida mientras la primera estaba bien.
func (s *StorageStatus) Primero() (HDD, bool) {
	if len(s.HDDs) == 0 {
		return HDD{}, false
	}
	for _, h := range s.HDDs {
		if !h.Healthy() {
			return h, true
		}
	}
	return s.HDDs[0], true
}

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
	// LoopEnable es la sobrescritura cíclica. Con `true` la cámara borra lo más viejo al
	// llenarse y graba para siempre; con `false` **deja de grabar** cuando se llena, y a
	// partir de ahí ningún evento tiene video.
	//
	// Es un puntero para distinguir "la cámara dijo false" de "el campo no vino": un
	// modelo que no lo reporte no debe disparar una alarma por omisión.
	LoopEnable *bool `xml:"LoopEnable"`
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

// LoopEnabledFrom lee la sobrescritura cíclica del XML crudo del track, para no pedir dos
// veces el mismo documento: el ciclo ya lo descarga para el horario de grabación.
//
// Devuelve (valor, true) si el campo vino, y (false, false) si no está o el XML no parsea.
func LoopEnabledFrom(raw []byte) (bool, bool) {
	var t Track
	if err := xml.Unmarshal(raw, &t); err != nil || t.LoopEnable == nil {
		return false, false
	}
	return *t.LoopEnable, true
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

// ScheduleCovers dice si TODAS las ventanas del track ya cubren [start, end]: empiezan a
// esa hora o antes y terminan a esa hora o después.
//
// Existe porque grabar de MÁS no es un problema y no justifica escribir. Medido el
// 2026-11-07 en la DS-2XM6825G1 (V4.2.2): trae 03:15:00-24:00:00, que cubre de sobra un
// 04:00-23:59 pedido, y además IGNORA cualquier PUT del horario respondiendo statusCode 1.
// Sin esta comprobación el ciclo escribía cada media hora un cambio innecesario que la
// cámara descartaba, y el log decía "corregida".
//
// Se compara por minuto, como SetScheduleWindow, y "24:00" vale como el fin del día
// (ordena después de "23:59" como texto, así que no hace falta tratarlo aparte). Un track
// sin ventanas no cubre nada.
func ScheduleCovers(raw []byte, start, end string) bool {
	if len(start) < 5 || len(end) < 5 {
		return false
	}
	s := string(raw)
	var horas []string
	for {
		abre := strings.Index(s, "<TimeOfDay>")
		if abre < 0 {
			break
		}
		cierra := strings.Index(s[abre:], "</TimeOfDay>")
		if cierra < 0 {
			break
		}
		horas = append(horas, s[abre+len("<TimeOfDay>"):abre+cierra])
		s = s[abre+cierra:]
	}
	if len(horas) == 0 || len(horas)%2 != 0 {
		return false
	}
	for i := 0; i < len(horas); i += 2 {
		ini, fin := horas[i], horas[i+1]
		if len(ini) < 5 || len(fin) < 5 || ini[:5] > start[:5] || fin[:5] < end[:5] {
			return false
		}
	}
	return true
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
