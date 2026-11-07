package isapi

import (
	"strings"
	"testing"
)

// trackCon arma un track con la misma ventana todos los días, como lo devuelve la cámara.
func trackCon(ini, fin string) []byte {
	var b strings.Builder
	b.WriteString(`<Track><TrackSchedule><ScheduleBlockList><ScheduleBlock>`)
	for _, d := range []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"} {
		b.WriteString(`<ScheduleAction><ScheduleActionStartTime><DayOfWeek>` + d +
			`</DayOfWeek><TimeOfDay>` + ini + `</TimeOfDay></ScheduleActionStartTime>` +
			`<ScheduleActionEndTime><DayOfWeek>` + d + `</DayOfWeek><TimeOfDay>` + fin +
			`</TimeOfDay></ScheduleActionEndTime></ScheduleAction>`)
	}
	b.WriteString(`</ScheduleBlock></ScheduleBlockList></TrackSchedule></Track>`)
	return []byte(b.String())
}

func TestScheduleCovers(t *testing.T) {
	for _, c := range []struct {
		nombre   string
		raw      []byte
		ini, fin string
		cubre    bool
	}{
		// Lo que trae la DS-2XM6825G1 (V4.2.2), que además ignora todo PUT del horario.
		{"la G1 de fábrica cubre el default", trackCon("03:15:00", "24:00:00"), "04:00:00", "23:59:00", true},
		{"igual al pedido", trackCon("04:00:00", "23:59:00"), "04:00:00", "23:59:59", true},
		{"24 h", trackCon("00:00:00", "24:00:00"), "04:00:00", "23:59:00", true},
		// La cámara de laboratorio venía así y dejaba media jornada sin video.
		{"empieza tarde", trackCon("07:55:00", "16:02:00"), "04:00:00", "23:59:00", false},
		{"termina antes", trackCon("04:00:00", "22:00:00"), "04:00:00", "23:59:00", false},
		{"sin ventanas", []byte(`<Track></Track>`), "04:00:00", "23:59:00", false},
	} {
		if got := ScheduleCovers(c.raw, c.ini, c.fin); got != c.cubre {
			t.Errorf("%s: ScheduleCovers=%v, se esperaba %v", c.nombre, got, c.cubre)
		}
	}
}

// Un solo día que no cubre alcanza para escribir: la regla es TODAS las ventanas.
func TestScheduleCoversExigeTodosLosDias(t *testing.T) {
	raw := strings.Replace(string(trackCon("03:15:00", "24:00:00")), "03:15:00", "09:00:00", 1)
	if ScheduleCovers([]byte(raw), "04:00:00", "23:59:00") {
		t.Error("el domingo empieza a las 09:00 y no cubre las 04:00")
	}
}
