package client

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Configuración que la plataforma publica en el atributo DEV_CAMERACOUNT_CONFIG.
//
// Acá vive SOLO la decisión de qué valor gana; el transporte está en client/parametros y
// la aplicación en counting-actor.go. Esa separación existe para que la regla de prioridad
// se pueda probar sin levantar MQTT ni protoactor remote, que es justamente lo que hoy no
// tiene prueba en ninguno de los proyectos de la flota que hacen esto.

// MaxDoorID acota el id explícito de puerta. Es una guarda contra un error de tipeo, no un
// límite modelado: escribir el puerto TCP donde va la puerta armaría un slice de miles de
// entradas y no contaría nada.
//
// Solo las puertas 0 y 1 aparecen en el registerMap de MQTT; una tercera se contaría
// internamente pero no se publicaría.
const MaxDoorID = 15

// Nombres de los flags que esta configuración puede fijar. Son las llaves del mapa de
// "fijados" que arma main con flag.Visit: un flag presente en ese mapa le gana a la
// plataforma.
//
// Van como constantes y no como literales sueltos porque el nombre tiene que coincidir
// EXACTAMENTE con el que registra el paquete flag. Un typo acá no da error de compilación
// ni de ejecución: simplemente hace que ese flag deje de proteger su valor, en silencio.
const (
	FlagCamera              = "camera"
	FlagZeroOpenState       = "zeroOpenState"
	FlagCountWithCloseDoor  = "countWithCloseDoor"
	FlagNtpServer           = "ntpServer"
	FlagVideoCodec          = "videoCodec"
	FlagSmartCodec          = "smartCodec"
	FlagVideoFrameRate      = "videoFrameRate"
	FlagVideoEnabled        = "videoEnabled"
	FlagVideoDir            = "videoDir"
	FlagCameraPowerGPIO     = "cameraPowerGPIO"
	FlagCameraPowerOffLevel = "cameraPowerOffLevel"
)

// CamaraConf es una cámara con su puerta y los dos switches de conteo de esa puerta.
//
// Los tres datos van JUNTOS a propósito. En la línea de comandos son tres flags repetibles
// —-camera, -zeroOpenState, -countWithCloseDoor— que hay que mantener alineados por
// posición, y desalinearlos no da ningún error: simplemente se descartan los cruces de una
// puerta durante todo un turno. Acá eso no se puede escribir mal.
type CamaraConf struct {
	IP     string `json:"ip"`
	Puerta *int   `json:"door"`
	// ZeroOpenState dice si el estado "abierta" llega como 0. Describe cómo está cableado
	// el vehículo.
	ZeroOpenState *bool `json:"zeroOpenState"`
	// CountWithCloseDoor cuenta aunque la puerta esté cerrada.
	CountWithCloseDoor *bool `json:"countWithCloseDoor"`
}

// Configuracion es el JSON que viaja dentro de DEV_CAMERACOUNT_CONFIG.
//
// "cameras" REEMPLAZA la lista entera, no es un parche: mandar una sola cámara deja la otra
// puerta sin cámara. Es lo que hace falta para poder quitar una desde la plataforma, pero
// conviene saberlo, porque usarlo mal no da ningún error — solo una serie de conteo que se
// congela.
//
// TODOS los campos escalares son punteros, y eso no es un detalle de estilo. En Go un bool
// ausente del JSON se deserializa como false, y un int ausente como 0; sin punteros no hay
// forma de distinguir "la plataforma no mandó este campo" de "la plataforma mandó false".
// Los binarios de la flota que resuelven esto mismo no hacen la distinción, y el resultado
// medido es que un objeto que OMITE un booleano lo apaga igual, pisando el flag con el que
// se arrancó el equipo. Con punteros, ausente es nil y no toca nada.
type Configuracion struct {
	Camaras             []CamaraConf `json:"cameras"`
	NtpServer           *string      `json:"ntpServer"`
	VideoCodec          *string      `json:"videoCodec"`
	SmartCodec          *string      `json:"smartCodec"`
	VideoFrameRate      *int         `json:"videoFrameRate"`
	VideoEnabled        *bool        `json:"videoEnabled"`
	VideoDir            *string      `json:"videoDir"`
	CameraPowerGPIO     *int         `json:"cameraPowerGPIO"`
	CameraPowerOffLevel *int         `json:"cameraPowerOffLevel"`
}

// Efectiva es la configuración ya resuelta: lo que el binario de verdad va a usar. No tiene
// punteros porque acá ya no hay "ausente", todo tiene un valor.
type Efectiva struct {
	// Camaras va indexado por puerta; "" significa que esa puerta no tiene cámara.
	Camaras            []string
	ZeroOpenState      map[int]bool
	CountWithCloseDoor map[int]bool

	NtpServer      string
	VideoCodec     string
	SmartCodec     string
	VideoFrameRate int

	VideoEnabled bool
	VideoDir     string

	CameraPowerGPIO     int
	CameraPowerOffLevel int
}

// Copia devuelve una copia profunda. Hace falta porque Efectiva lleva mapas y un slice: sin
// esto, resolver una configuración nueva mutaría la anterior y el Diff siempre diría que no
// cambió nada.
func (e Efectiva) Copia() Efectiva {
	out := e
	out.Camaras = append([]string(nil), e.Camaras...)
	out.ZeroOpenState = make(map[int]bool, len(e.ZeroOpenState))
	for k, v := range e.ZeroOpenState {
		out.ZeroOpenState[k] = v
	}
	out.CountWithCloseDoor = make(map[int]bool, len(e.CountWithCloseDoor))
	for k, v := range e.CountWithCloseDoor {
		out.CountWithCloseDoor[k] = v
	}
	return out
}

// CamaraDe devuelve la IP de una puerta, o "" si no tiene.
func (e Efectiva) CamaraDe(puerta int) string {
	if puerta < 0 || puerta >= len(e.Camaras) {
		return ""
	}
	return e.Camaras[puerta]
}

// PrimeraCamara es la que consulta el keep-alive. Vacío conserva la dirección histórica.
func (e Efectiva) PrimeraCamara() string {
	for _, ip := range e.Camaras {
		if ip != "" {
			return ip
		}
	}
	return ""
}

// HayCamaras dice si alguna puerta tiene cámara configurada.
func (e Efectiva) HayCamaras() bool { return e.PrimeraCamara() != "" }

// ParseConfiguracion lee el JSON del atributo y lo valida entero.
//
// Valida ANTES de devolver, y falla completo: una configuración a medias es peor que la
// anterior, porque deja el equipo en un estado que nadie escribió. Quien llama conserva la
// que ya tenía.
func ParseConfiguracion(raw []byte) (*Configuracion, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, fmt.Errorf("configuración vacía")
	}
	cfg := new(Configuracion)
	// DisallowUnknownFields NO se usa a propósito: un campo nuevo agregado por la
	// plataforma para una versión futura del binario no debe tumbar la configuración
	// entera en los equipos viejos.
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	if err := cfg.Validar(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validar rechaza lo que no se puede aplicar, nombrando el campo.
func (c *Configuracion) Validar() error {
	vistas := make(map[int]string, len(c.Camaras))
	for i, cam := range c.Camaras {
		ip := strings.TrimSpace(cam.IP)
		if ip == "" {
			return fmt.Errorf("cameras[%d]: \"ip\" vacía", i)
		}
		// Un puerto en la dirección no se admite: el HTTP de ISAPI lo respetaría, pero la
		// extracción de video usa RTSP en 554 y quedaría mal armada.
		if strings.Contains(ip, ":") {
			return fmt.Errorf("cameras[%d]: %q lleva puerto; la extracción usa RTSP en 554 "+
				"y la puerta va en \"door\"", i, ip)
		}
		if cam.Puerta == nil {
			return fmt.Errorf("cameras[%d] (%s): falta \"door\"; sin la puerta no se sabe a "+
				"qué contador va el pasajero", i, ip)
		}
		if *cam.Puerta < 0 || *cam.Puerta > MaxDoorID {
			return fmt.Errorf("cameras[%d] (%s): \"door\" %d fuera de 0..%d",
				i, ip, *cam.Puerta, MaxDoorID)
		}
		if otra, ok := vistas[*cam.Puerta]; ok {
			return fmt.Errorf("cameras: la puerta %d está repetida (%s y %s); una cámara "+
				"taparía a la otra en silencio", *cam.Puerta, otra, ip)
		}
		vistas[*cam.Puerta] = ip
	}
	if c.VideoCodec != nil {
		if _, err := NormalizeVideoCodec(*c.VideoCodec); err != nil {
			return fmt.Errorf("videoCodec: %w", err)
		}
	}
	if c.SmartCodec != nil {
		if _, err := NormalizeSmartCodec(*c.SmartCodec); err != nil {
			return fmt.Errorf("smartCodec: %w", err)
		}
	}
	if c.VideoFrameRate != nil && *c.VideoFrameRate < 0 {
		return fmt.Errorf("videoFrameRate %d no puede ser negativo", *c.VideoFrameRate)
	}
	if c.CameraPowerGPIO != nil && *c.CameraPowerGPIO < 0 {
		return fmt.Errorf("cameraPowerGPIO %d no puede ser negativo", *c.CameraPowerGPIO)
	}
	if c.CameraPowerOffLevel != nil && *c.CameraPowerOffLevel != 0 && *c.CameraPowerOffLevel != 1 {
		return fmt.Errorf("cameraPowerOffLevel %d inválido, solo 0 o 1", *c.CameraPowerOffLevel)
	}
	if c.VideoDir != nil {
		d := strings.TrimSpace(*c.VideoDir)
		if d != "" && !strings.HasPrefix(d, "/") {
			return fmt.Errorf("videoDir %q tiene que ser una ruta absoluta", *c.VideoDir)
		}
	}
	return nil
}

// Resolver combina la configuración de la plataforma sobre la base que salió de los flags y
// los defaults, y devuelve lo que el binario va a usar.
//
// LA REGLA DE PRIORIDAD, que es deliberadamente la INVERSA de la del resto de la flota:
//
//  1. flag pasado explícitamente en la línea de comandos  -> gana siempre
//  2. plataforma                                          -> solo rellena lo que el flag no fijó
//  3. default compilado
//
// En go-driverconsole y go-counterpass la plataforma pisa el flag sin preguntar, porque el
// merge compara contra el valor actual de la variable y no sabe de dónde salió. Acá sí se
// sabe: `fijados` lo arma main con flag.Visit, que recorre SOLO los flags realmente
// pasados. No se puede deducir del valor —`-videoFrameRate 0` y "no lo pasaron" dan la
// misma variable— y esa confusión es justo la que rompe el merge de los otros binarios.
//
// La consecuencia hay que tenerla presente: un lanzador que pase el flag deja ese parámetro
// fuera del alcance de la plataforma. Por eso Describir() dice, parámetro por parámetro, de
// dónde salió el valor.
func Resolver(base Efectiva, p *Configuracion, fijados map[string]bool) Efectiva {
	out := base.Copia()
	if p == nil {
		return out
	}
	fijado := func(flag string) bool { return fijados != nil && fijados[flag] }

	// La lista de cámaras y los switches por puerta se resuelven por separado aunque
	// lleguen en el mismo objeto: un equipo puede traer -camera en el lanzador y dejar que
	// la plataforma maneje la polaridad de las puertas, o al revés.
	if len(p.Camaras) > 0 {
		if !fijado(FlagCamera) {
			mayor := -1
			for _, cam := range p.Camaras {
				if *cam.Puerta > mayor {
					mayor = *cam.Puerta
				}
			}
			// Los ids que nadie reclama quedan vacíos, y todo el binario ya los salta:
			// doorID no los compara, CameraActor no los revisa y VideoActor no extrae.
			lista := make([]string, mayor+1)
			for _, cam := range p.Camaras {
				lista[*cam.Puerta] = strings.TrimSpace(cam.IP)
			}
			out.Camaras = lista
		}
		for _, cam := range p.Camaras {
			if cam.ZeroOpenState != nil && !fijado(FlagZeroOpenState) {
				out.ZeroOpenState[*cam.Puerta] = *cam.ZeroOpenState
			}
			if cam.CountWithCloseDoor != nil && !fijado(FlagCountWithCloseDoor) {
				out.CountWithCloseDoor[*cam.Puerta] = *cam.CountWithCloseDoor
			}
		}
	}

	if p.NtpServer != nil && !fijado(FlagNtpServer) {
		out.NtpServer = strings.TrimSpace(*p.NtpServer)
	}
	if p.VideoCodec != nil && !fijado(FlagVideoCodec) {
		// Ya pasó por Validar, así que el error es imposible acá; se ignora para no
		// propagar un error que no puede ocurrir.
		v, _ := NormalizeVideoCodec(*p.VideoCodec)
		out.VideoCodec = v
	}
	if p.SmartCodec != nil && !fijado(FlagSmartCodec) {
		v, _ := NormalizeSmartCodec(*p.SmartCodec)
		out.SmartCodec = v
	}
	if p.VideoFrameRate != nil && !fijado(FlagVideoFrameRate) {
		out.VideoFrameRate = *p.VideoFrameRate
	}
	if p.VideoEnabled != nil && !fijado(FlagVideoEnabled) {
		out.VideoEnabled = *p.VideoEnabled
	}
	if p.VideoDir != nil && !fijado(FlagVideoDir) {
		out.VideoDir = strings.TrimSpace(*p.VideoDir)
	}
	if p.CameraPowerGPIO != nil && !fijado(FlagCameraPowerGPIO) {
		out.CameraPowerGPIO = *p.CameraPowerGPIO
	}
	if p.CameraPowerOffLevel != nil && !fijado(FlagCameraPowerOffLevel) {
		out.CameraPowerOffLevel = *p.CameraPowerOffLevel
	}
	return out
}

// Cambios dice qué hay que rehacer. Está agrupado por ACTOR y no por campo, porque lo que
// cuesta es reconstruir un actor, no leer un valor.
type Cambios struct {
	// Camaras: la lista o su asignación a puertas. Toca a todos los que le hablan a la
	// cámara y al que clasifica los eventos entrantes.
	Camaras bool
	// Puertas: polaridad y conteo con puerta cerrada. Se aplica en el lugar, sin respawn.
	Puertas bool
	// Camara: NTP y perfil de codificación.
	Camara bool
	// Video: habilitación y directorio.
	Video bool
	// GPIO: línea o nivel de apagado de la cámara.
	GPIO bool
}

// Hay dice si algo cambió.
func (c Cambios) Hay() bool {
	return c.Camaras || c.Puertas || c.Camara || c.Video || c.GPIO
}

// Describir arma la línea del log de un cambio.
func (c Cambios) Describir() string {
	partes := make([]string, 0, 5)
	if c.Camaras {
		partes = append(partes, "cámaras")
	}
	if c.Puertas {
		partes = append(partes, "puertas")
	}
	if c.Camara {
		partes = append(partes, "mantenimiento de cámara")
	}
	if c.Video {
		partes = append(partes, "video")
	}
	if c.GPIO {
		partes = append(partes, "gpio de alimentación")
	}
	if len(partes) == 0 {
		return "nada"
	}
	return strings.Join(partes, ", ")
}

// Diff compara la configuración aplicada con una nueva.
func (e Efectiva) Diff(n Efectiva) Cambios {
	var c Cambios
	if !mismasCamaras(e.Camaras, n.Camaras) {
		c.Camaras = true
	}
	if !mismoMapaBool(e.ZeroOpenState, n.ZeroOpenState) ||
		!mismoMapaBool(e.CountWithCloseDoor, n.CountWithCloseDoor) {
		c.Puertas = true
	}
	if e.NtpServer != n.NtpServer || e.VideoCodec != n.VideoCodec ||
		e.SmartCodec != n.SmartCodec || e.VideoFrameRate != n.VideoFrameRate {
		c.Camara = true
	}
	if e.VideoEnabled != n.VideoEnabled || e.VideoDir != n.VideoDir {
		c.Video = true
	}
	if e.CameraPowerGPIO != n.CameraPowerGPIO || e.CameraPowerOffLevel != n.CameraPowerOffLevel {
		c.GPIO = true
	}
	return c
}

// mismasCamaras compara ignorando las puertas vacías del final: ["a"] y ["a",""] describen
// exactamente lo mismo y no deben disparar un respawn.
func mismasCamaras(a, b []string) bool {
	recorte := func(s []string) []string {
		for len(s) > 0 && s[len(s)-1] == "" {
			s = s[:len(s)-1]
		}
		return s
	}
	a, b = recorte(a), recorte(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mismoMapaBool(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// Describir arma el banner de arranque y el log de cada aplicación: por parámetro, el valor
// y de dónde salió.
//
// No es cosmético. Con la prioridad invertida, un flag en el lanzador deja ese parámetro
// fuera del alcance de la plataforma, y sin esta línea eso se descubre recién cuando alguien
// cambia un valor desde la plataforma y el equipo no le hace caso.
func (e Efectiva) Describir(fijados map[string]bool) string {
	origen := func(flag string) string {
		if fijados != nil && fijados[flag] {
			return "flag"
		}
		return "plataforma/default"
	}
	var b strings.Builder
	puertas := make([]int, 0, len(e.Camaras))
	for id, ip := range e.Camaras {
		if ip != "" {
			puertas = append(puertas, id)
		}
	}
	sort.Ints(puertas)
	if len(puertas) == 0 {
		fmt.Fprintf(&b, "  %-20s %-28s [%s]\n", "cámaras", "ninguna", origen(FlagCamera))
	}
	for _, id := range puertas {
		fmt.Fprintf(&b, "  %-20s %-28s [%s]  zeroOpenState=%-5v [%s]  countWithCloseDoor=%-5v [%s]\n",
			fmt.Sprintf("puerta %d", id), e.Camaras[id], origen(FlagCamera),
			e.ZeroOpenState[id], origen(FlagZeroOpenState),
			e.CountWithCloseDoor[id], origen(FlagCountWithCloseDoor))
	}
	linea := func(etiqueta, valor, flag string) {
		fmt.Fprintf(&b, "  %-20s %-28s [%s]\n", etiqueta, valor, origen(flag))
	}
	linea("ntpServer", vacioComoGuion(e.NtpServer), FlagNtpServer)
	linea("videoCodec", vacioComoGuion(e.VideoCodec), FlagVideoCodec)
	linea("smartCodec", vacioComoGuion(e.SmartCodec), FlagSmartCodec)
	linea("videoFrameRate", strconv.Itoa(e.VideoFrameRate), FlagVideoFrameRate)
	linea("videoEnabled", strconv.FormatBool(e.VideoEnabled), FlagVideoEnabled)
	linea("videoDir", vacioComoGuion(e.VideoDir), FlagVideoDir)
	linea("cameraPowerGPIO", strconv.Itoa(e.CameraPowerGPIO), FlagCameraPowerGPIO)
	linea("cameraPowerOffLevel", strconv.Itoa(e.CameraPowerOffLevel), FlagCameraPowerOffLevel)
	return strings.TrimRight(b.String(), "\n")
}

func vacioComoGuion(v string) string {
	if v == "" {
		return "-"
	}
	return v
}
