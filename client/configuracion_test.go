package client

import (
	"strings"
	"testing"
	"time"
)

// Fixture con la forma exacta que la plataforma publica dentro de
// DEV_CAMERACOUNT_CONFIG. Va completo a propósito: si alguien le cambia una llave al
// contrato, esto falla acá y no en el vehículo.
const jsonPlataforma = `{
  "cameras": [
    {"ip": "192.168.186.93", "door": 0, "zeroOpenState": true, "countWithCloseDoor": true},
    {"ip": "192.168.186.91", "door": 1, "zeroOpenState": true, "countWithCloseDoor": false}
  ],
  "ntpServer": "ntp1.inm.gov.co",
  "videoCodec": "h265",
  "smartCodec": "off",
  "videoFrameRate": 12,
  "videoEnabled": true,
  "videoDir": "/SD/video265/",
  "cameraPowerGPIO": 165,
  "cameraPowerOffLevel": 1
}`

// base arma la configuración que saldría de los defaults, sin ningún flag.
func base() Efectiva {
	return Efectiva{
		Camaras:             nil,
		ZeroOpenState:       map[int]bool{},
		CountWithCloseDoor:  map[int]bool{},
		VideoEnabled:        true,
		CameraPowerOffLevel: 1,
	}
}

func parsear(t *testing.T, raw string) *Configuracion {
	t.Helper()
	cfg, err := ParseConfiguracion([]byte(raw))
	if err != nil {
		t.Fatalf("no debería fallar: %s", err)
	}
	return cfg
}

// TestPlataformaRellenaLoQueElFlagNoFijo es el camino feliz: sin flags, la plataforma
// configura el equipo entero.
func TestPlataformaRellenaLoQueElFlagNoFijo(t *testing.T) {
	e := Resolver(base(), parsear(t, jsonPlataforma), nil)

	if got := e.CamaraDe(0); got != "192.168.186.93" {
		t.Errorf("puerta 0 = %q, quiero 192.168.186.93", got)
	}
	if got := e.CamaraDe(1); got != "192.168.186.91" {
		t.Errorf("puerta 1 = %q, quiero 192.168.186.91", got)
	}
	if !e.ZeroOpenState[0] || !e.ZeroOpenState[1] {
		t.Errorf("zeroOpenState = %v, quiero las dos en true", e.ZeroOpenState)
	}
	if !e.CountWithCloseDoor[0] {
		t.Error("countWithCloseDoor de la puerta 0 debería ser true")
	}
	if e.CountWithCloseDoor[1] {
		t.Error("countWithCloseDoor de la puerta 1 debería ser false")
	}
	if e.NtpServer != "ntp1.inm.gov.co" {
		t.Errorf("ntpServer = %q", e.NtpServer)
	}
	// El codec se normaliza al valor que espera ISAPI, no al que escribió la plataforma.
	if e.VideoCodec != "H.265" {
		t.Errorf("videoCodec = %q, quiero H.265", e.VideoCodec)
	}
	if e.SmartCodec != "off" || e.VideoFrameRate != 12 {
		t.Errorf("smartCodec=%q fps=%d", e.SmartCodec, e.VideoFrameRate)
	}
	if !e.VideoEnabled || e.VideoDir != "/SD/video265/" {
		t.Errorf("video: enabled=%v dir=%q", e.VideoEnabled, e.VideoDir)
	}
	if e.CameraPowerGPIO != 165 || e.CameraPowerOffLevel != 1 {
		t.Errorf("gpio=%d nivel=%d", e.CameraPowerGPIO, e.CameraPowerOffLevel)
	}
}

// TestElFlagExplicitoLeGanaALaPlataforma fija la decisión de diseño que separa a este
// binario del resto de la flota. Si alguien la invierte "para alinearlo", esto falla.
func TestElFlagExplicitoLeGanaALaPlataforma(t *testing.T) {
	b := base()
	b.VideoFrameRate = 20
	b.NtpServer = "ntp.local"
	b.VideoCodec = "H.264"

	fijados := map[string]bool{
		FlagVideoFrameRate: true,
		FlagNtpServer:      true,
	}
	e := Resolver(b, parsear(t, jsonPlataforma), fijados)

	if e.VideoFrameRate != 20 {
		t.Errorf("videoFrameRate = %d, el flag explícito decía 20", e.VideoFrameRate)
	}
	if e.NtpServer != "ntp.local" {
		t.Errorf("ntpServer = %q, el flag explícito decía ntp.local", e.NtpServer)
	}
	// videoCodec NO estaba fijado por flag, así que la plataforma sí lo mueve aunque la
	// base traiga un valor distinto de cero.
	if e.VideoCodec != "H.265" {
		t.Errorf("videoCodec = %q, nadie lo fijó por flag: debía ganar la plataforma", e.VideoCodec)
	}
}

// TestFlagDeCamaraFijaLaListaPeroNoLaPolaridad: son decisiones separadas. Un equipo puede
// traer -camera en el lanzador y dejar que la plataforma maneje cómo está cableada la
// puerta, que es lo que de verdad cambia entre vehículos.
func TestFlagDeCamaraFijaLaListaPeroNoLaPolaridad(t *testing.T) {
	b := base()
	b.Camaras = []string{"10.0.0.1", "10.0.0.2"}

	e := Resolver(b, parsear(t, jsonPlataforma), map[string]bool{FlagCamera: true})

	if e.CamaraDe(0) != "10.0.0.1" || e.CamaraDe(1) != "10.0.0.2" {
		t.Errorf("la lista de cámaras la fijó el flag y no debía moverse: %v", e.Camaras)
	}
	if !e.ZeroOpenState[0] || !e.ZeroOpenState[1] {
		t.Errorf("zeroOpenState = %v, la plataforma sí debía aplicarlo", e.ZeroOpenState)
	}
}

// TestCampoAusenteNoPisaNada es la corrección del defecto medido en los binarios de
// referencia: allá un booleano OMITIDO del JSON apaga el valor con el que se arrancó.
func TestCampoAusenteNoPisaNada(t *testing.T) {
	b := base()
	b.VideoEnabled = true
	b.VideoFrameRate = 12
	b.NtpServer = "ntp.local"
	b.CameraPowerGPIO = 165

	// Un objeto que solo trae el codec. Todo lo demás está AUSENTE, no en false/0.
	e := Resolver(b, parsear(t, `{"videoCodec":"h264"}`), nil)

	if !e.VideoEnabled {
		t.Error("videoEnabled se apagó con un campo que no venía en el JSON")
	}
	if e.VideoFrameRate != 12 {
		t.Errorf("videoFrameRate = %d, el campo no venía en el JSON", e.VideoFrameRate)
	}
	if e.NtpServer != "ntp.local" {
		t.Errorf("ntpServer = %q, el campo no venía en el JSON", e.NtpServer)
	}
	if e.CameraPowerGPIO != 165 {
		t.Errorf("cameraPowerGPIO = %d, el campo no venía en el JSON", e.CameraPowerGPIO)
	}
	if e.VideoCodec != "H.264" {
		t.Errorf("videoCodec = %q, ese sí venía", e.VideoCodec)
	}
}

// TestFalseExplicitoSiApaga es la otra mitad de la anterior, y la razón de que los campos
// sean punteros: "ausente" y "false" tienen que poder distinguirse en las dos direcciones.
func TestFalseExplicitoSiApaga(t *testing.T) {
	b := base()
	b.VideoEnabled = true

	e := Resolver(b, parsear(t, `{"videoEnabled":false}`), nil)
	if e.VideoEnabled {
		t.Error("videoEnabled: un false EXPLÍCITO sí tiene que apagar la extracción")
	}
}

// TestApagarElVideoDesdeLaPlataforma: el interruptor que se va a usar todos los días.
func TestApagarElVideoDesdeLaPlataforma(t *testing.T) {
	b := base()
	b.VideoEnabled = true
	b.VideoDir = "/SD/video265/"

	apagada := Resolver(b, parsear(t, `{"videoEnabled":false}`), nil)
	if cambios := b.Diff(apagada); !cambios.Video {
		t.Error("apagar el video tiene que marcar el cambio de video")
	} else if cambios.Camaras || cambios.Puertas || cambios.Camara || cambios.GPIO {
		t.Errorf("apagar el video no debe tocar nada más: %+v", cambios)
	}
	// El directorio se conserva, para poder volver a prenderlo sin reconfigurarlo.
	if apagada.VideoDir != "/SD/video265/" {
		t.Errorf("videoDir = %q, no debía borrarse al apagar", apagada.VideoDir)
	}
}

// TestDiffAgrupaPorActor: lo que cuesta es reconstruir un actor, así que el diff tiene que
// decir exactamente cuáles, y ninguno de más.
func TestDiffAgrupaPorActor(t *testing.T) {
	previa := Resolver(base(), parsear(t, jsonPlataforma), nil)

	casos := []struct {
		nombre string
		json   string
		quiero Cambios
	}{
		{"lo mismo otra vez", jsonPlataforma, Cambios{}},
		{"solo los fps", `{"videoFrameRate":20}`, Cambios{Camara: true}},
		{"solo el ntp", `{"ntpServer":"otro.ntp"}`, Cambios{Camara: true}},
		{"solo el video", `{"videoEnabled":false}`, Cambios{Video: true}},
		{"solo la gpio", `{"cameraPowerGPIO":164}`, Cambios{GPIO: true}},
		{"solo la polaridad", `{"cameras":[{"ip":"192.168.186.93","door":0,"zeroOpenState":false,"countWithCloseDoor":true},
			{"ip":"192.168.186.91","door":1,"zeroOpenState":true,"countWithCloseDoor":false}]}`,
			Cambios{Puertas: true}},
		{"solo la ip", `{"cameras":[{"ip":"192.168.186.99","door":0,"zeroOpenState":true,"countWithCloseDoor":true},
			{"ip":"192.168.186.91","door":1,"zeroOpenState":true,"countWithCloseDoor":false}]}`,
			Cambios{Camaras: true}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			nueva := Resolver(previa, parsear(t, c.json), nil)
			if got := previa.Diff(nueva); got != c.quiero {
				t.Errorf("Diff = %+v, quiero %+v", got, c.quiero)
			}
		})
	}
}

// TestLaListaDeCamarasEsCompletaNoUnParche. "cameras" reemplaza la lista entera: mandar
// una sola cámara BORRA la otra puerta. Es la semántica correcta —así se quita una cámara
// desde la plataforma— pero es fácil de usar mal creyendo que se está tocando solo una, y
// el síntoma sería una serie de conteo congelándose sin ningún error.
func TestLaListaDeCamarasEsCompletaNoUnParche(t *testing.T) {
	previa := Resolver(base(), parsear(t, jsonPlataforma), nil)
	if previa.CamaraDe(1) == "" {
		t.Fatal("la base del caso debía tener dos puertas")
	}

	nueva := Resolver(previa, parsear(t, `{"cameras":[{"ip":"192.168.186.93","door":0}]}`), nil)
	if nueva.CamaraDe(1) != "" {
		t.Errorf("la puerta 1 debía quedar sin cámara, quedó %q", nueva.CamaraDe(1))
	}
	if !previa.Diff(nueva).Camaras {
		t.Error("quitar una cámara tiene que marcar el cambio de cámaras")
	}
}

// TestResolverNoMutaLaBase: sin la copia profunda, resolver una configuración nueva
// mutaría la anterior y el Diff diría siempre que no cambió nada — un binario que nunca
// aplica nada y no da ningún error.
func TestResolverNoMutaLaBase(t *testing.T) {
	b := base()
	b.Camaras = []string{"10.0.0.1"}
	b.ZeroOpenState[0] = false

	_ = Resolver(b, parsear(t, jsonPlataforma), nil)

	if b.CamaraDe(0) != "10.0.0.1" {
		t.Errorf("la base quedó mutada: %v", b.Camaras)
	}
	if b.ZeroOpenState[0] {
		t.Error("la base quedó mutada en zeroOpenState")
	}
}

// TestConfiguracionInvalidaSeRechazaEntera. Nunca a medias: una configuración aplicada a
// medias deja el equipo en un estado que nadie escribió.
func TestConfiguracionInvalidaSeRechazaEntera(t *testing.T) {
	casos := []struct {
		nombre string
		json   string
		dice   string
	}{
		{"JSON roto", `{"videoCodec":`, "JSON inválido"},
		{"vacío", "   ", "vacía"},
		{"codec que no existe", `{"videoCodec":"h266"}`, "videoCodec"},
		{"smartCodec que no existe", `{"smartCodec":"quizás"}`, "smartCodec"},
		{"nivel de apagado fuera de rango", `{"cameraPowerOffLevel":2}`, "cameraPowerOffLevel"},
		{"gpio negativa", `{"cameraPowerGPIO":-1}`, "cameraPowerGPIO"},
		{"fps negativos", `{"videoFrameRate":-1}`, "videoFrameRate"},
		{"videoDir relativo", `{"videoDir":"video/"}`, "absoluta"},
		{"cámara sin ip", `{"cameras":[{"door":0}]}`, "ip"},
		{"cámara sin puerta", `{"cameras":[{"ip":"10.0.0.1"}]}`, "door"},
		{"puerta fuera de rango", `{"cameras":[{"ip":"10.0.0.1","door":99}]}`, "fuera de 0.."},
		{"puerto en la ip", `{"cameras":[{"ip":"10.0.0.1:8080","door":0}]}`, "puerto"},
		{"puerta repetida", `{"cameras":[{"ip":"10.0.0.1","door":0},{"ip":"10.0.0.2","door":0}]}`,
			"repetida"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			_, err := ParseConfiguracion([]byte(c.json))
			if err == nil {
				t.Fatalf("%s debería dar error", c.json)
			}
			if !strings.Contains(err.Error(), c.dice) {
				t.Errorf("el error no nombra el problema (%q): %s", c.dice, err)
			}
		})
	}
}

// TestCampoDesconocidoNoTumbaLaConfiguracion: la plataforma puede agregar un campo para una
// versión futura del binario, y los equipos viejos tienen que seguir contando.
func TestCampoDesconocidoNoTumbaLaConfiguracion(t *testing.T) {
	cfg, err := ParseConfiguracion([]byte(`{"videoFrameRate":12,"algoQueNoExisteTodavia":{"a":1}}`))
	if err != nil {
		t.Fatalf("no debería fallar: %s", err)
	}
	if cfg.VideoFrameRate == nil || *cfg.VideoFrameRate != 12 {
		t.Error("el campo conocido se tenía que leer igual")
	}
}

// TestDescribirDiceDeDondeSalioCadaValor. Con la prioridad invertida, un flag en el
// lanzador deja ese parámetro fuera del alcance de la plataforma; sin esta línea en el log
// eso se descubre recién cuando alguien cambia el valor y el equipo no le hace caso.
func TestDescribirDiceDeDondeSalioCadaValor(t *testing.T) {
	fijados := map[string]bool{FlagVideoFrameRate: true}
	e := Resolver(base(), parsear(t, jsonPlataforma), fijados)
	txt := e.Describir(fijados)

	if !strings.Contains(txt, "videoFrameRate") || !strings.Contains(txt, "[flag]") {
		t.Errorf("el banner no marca el flag explícito:\n%s", txt)
	}
	if !strings.Contains(txt, "[plataforma/default]") {
		t.Errorf("el banner no marca lo que vino de la plataforma:\n%s", txt)
	}
	if !strings.Contains(txt, "192.168.186.93") {
		t.Errorf("el banner no lista las cámaras:\n%s", txt)
	}
}

// La ráfaga se configura en segundos enteros desde la plataforma, y el flag explícito gana.
func TestRafagaDesdeLaPlataforma(t *testing.T) {
	b := base()
	b.VideoBurstGap, b.VideoMaxDuration = 10*time.Second, 60*time.Second

	e := Resolver(b, parsear(t, `{"videoBurstGap":8,"videoMaxDuration":45}`), nil)
	if e.VideoBurstGap != 8*time.Second || e.VideoMaxDuration != 45*time.Second {
		t.Errorf("la plataforma debía fijar 8 s y 45 s: %v %v", e.VideoBurstGap, e.VideoMaxDuration)
	}
	if !b.Diff(e).Video || b.Diff(e).Camara {
		t.Errorf("cambiar la ráfaga rehace solo el video: %+v", b.Diff(e))
	}

	e = Resolver(b, parsear(t, `{"videoBurstGap":0}`), map[string]bool{FlagVideoMaxDuration: true})
	if e.VideoBurstGap != 0 {
		t.Errorf("un 0 explícito apaga el encadenamiento: %v", e.VideoBurstGap)
	}
	e = Resolver(b, parsear(t, `{"videoMaxDuration":30}`), map[string]bool{FlagVideoMaxDuration: true})
	if e.VideoMaxDuration != 60*time.Second {
		t.Errorf("con -videoMaxDuration en la línea de comandos la plataforma no lo mueve: %v", e.VideoMaxDuration)
	}
	if e = Resolver(b, parsear(t, `{}`), nil); e.VideoBurstGap != 10*time.Second {
		t.Errorf("ausente no toca nada: %v", e.VideoBurstGap)
	}
}

func TestRafagaFueraDeRangoSeRechaza(t *testing.T) {
	for _, raw := range []string{`{"videoBurstGap":-1}`, `{"videoMaxDuration":60000}`} {
		if _, err := ParseConfiguracion([]byte(raw)); err == nil {
			t.Errorf("%s debía rechazarse", raw)
		}
	}
}
