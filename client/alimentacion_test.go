package client

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

// contadorMudo arma un CountingActor con la alimentación habilitada sobre un sysfs
// falso, sin hijos y sin logs. Sin hijos, avisarAlimentacion no toca el contexto, así
// que la política se puede ejercitar sin levantar un sistema de actores con su
// proveedor de persistencia.
func contadorMudo(t *testing.T, nivelApagado int) (*CountingActor, *SalidaGPIO, *[]interface{}, *[]string) {
	t.Helper()
	nivelEncendido := 1
	if nivelApagado == 1 {
		nivelEncendido = 0
	}
	raiz := sysfsFalso(t, 164, nivelEncendido)
	salida, err := nuevaSalidaGPIOEn(raiz, 164, nivelEncendido)
	if err != nil {
		t.Fatal(err)
	}

	a := NewCountingActor()
	mudo := log.New(ioutil.Discard, "", 0)
	a.SetLogError(mudo).SetLogWarn(mudo).SetLogInfo(mudo).SetLogBuild(mudo)
	a.initLogs()
	a.SetCameraPower(salida, nivelApagado, time.Minute, time.Minute)

	programados := new([]interface{})
	a.programar = func(_ actor.Context, _ time.Duration, msg interface{}) {
		*programados = append(*programados, msg)
	}
	// Sin esto la prueba llamaría al Publish real, que levanta el cliente de MQTT e
	// intenta conectarse a 127.0.0.1:1883. Una prueba unitaria no puede depender de que
	// haya un broker, y además ese camino tiene una carrera conocida en el singleton de
	// pubsub.go que no es de esta función.
	publicados := new([]string)
	a.publicar = func(_ string, data []byte) { *publicados = append(*publicados, string(data)) }
	return a, salida, programados, publicados
}

func nivelDe(t *testing.T, s *SalidaGPIO) int {
	t.Helper()
	v, err := s.Leer()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func suspend(encendido bool) *MsgIgnition {
	return &MsgIgnition{Tipo: tipoSuspendEvt, Encendido: encendido, Timestamp: time.Now()}
}

// TestAlimentacionCicloCompleto: apagar el vehículo programa el corte, el corte baja la
// línea, y encender la devuelve de inmediato.
func TestAlimentacionCicloCompleto(t *testing.T) {
	a, salida, programados, publicados := contadorMudo(t, 1)

	if got := nivelDe(t, salida); got != 0 {
		t.Fatalf("al arrancar la linea debe quedar en encendida (0), esta en %d", got)
	}

	a.aplicarAlimentacion(nil, suspend(false))
	if len(*programados) != 1 {
		t.Fatalf("el apagado debe programar el corte, programados=%d", len(*programados))
	}
	if _, ok := (*programados)[0].(*msgCortarAlimentacion); !ok {
		t.Fatalf("se programó %T, quiero *msgCortarAlimentacion", (*programados)[0])
	}
	// Todavía no se cortó: el plazo existe para que termine el clip en curso.
	if got := nivelDe(t, salida); got != 0 {
		t.Errorf("la linea no se debe cortar antes del plazo, esta en %d", got)
	}

	a.cortarAlimentacion()
	if got := nivelDe(t, salida); got != 1 {
		t.Errorf("tras el corte la linea debe quedar en apagada (1), esta en %d", got)
	}

	// El CAMERAPOWER es lo que le permite a la plataforma distinguir "apagada a
	// propósito" de "averiada": las dos dejan de contar y las dos se quedan calladas.
	// Por eso su forma se fija acá y no solo en la documentación.
	if len(*publicados) != 1 {
		t.Fatalf("el corte debe publicar un CAMERAPOWER, publicados=%d", len(*publicados))
	}
	var evt struct {
		Type  string `json:"type"`
		Value struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			GPIO   int    `json:"gpio"`
			Level  int    `json:"level"`
			Source string `json:"source"`
		} `json:"value"`
		Timestamp float64 `json:"timestamp"`
	}
	if err := json.Unmarshal([]byte((*publicados)[0]), &evt); err != nil {
		t.Fatalf("el CAMERAPOWER no es JSON válido: %s", err)
	}
	if evt.Type != "CAMERAPOWER" {
		t.Errorf("type = %q, quiero \"CAMERAPOWER\"", evt.Type)
	}
	if evt.Value.Status != "poweroff" {
		t.Errorf("status = %q, quiero \"poweroff\"", evt.Value.Status)
	}
	if evt.Value.GPIO != 164 || evt.Value.Level != 1 {
		t.Errorf("gpio=%d level=%d, quiero 164 y 1", evt.Value.GPIO, evt.Value.Level)
	}
	if evt.Value.Source != "ignition" {
		t.Errorf("source = %q, quiero \"ignition\"", evt.Value.Source)
	}
	if evt.Timestamp <= 0 {
		t.Errorf("timestamp = %v, debe ser el reloj actual en segundos", evt.Timestamp)
	}

	a.aplicarAlimentacion(nil, suspend(true))
	if got := nivelDe(t, salida); got != 0 {
		t.Errorf("al encender la linea vuelve enseguida a encendida (0), esta en %d", got)
	}
	if len(*programados) != 2 {
		t.Fatalf("el encendido debe programar la espera de arranque, programados=%d", len(*programados))
	}
	if _, ok := (*programados)[1].(*msgCamaraLista); !ok {
		t.Fatalf("se programó %T, quiero *msgCamaraLista", (*programados)[1])
	}

	// El "poweron" NO sale al devolver la energía sino cuando la cámara ya tuvo tiempo
	// de arrancar: anunciarla disponible antes dejaría a la plataforma esperando
	// conteos durante los dos minutos del boot.
	if len(*publicados) != 1 {
		t.Errorf("todavía no debe haber un poweron, publicados=%d", len(*publicados))
	}
	a.camaraTerminoDeArrancar(nil)
	if len(*publicados) != 2 {
		t.Fatalf("tras el arranque debe publicarse el poweron, publicados=%d", len(*publicados))
	}
	if !strings.Contains((*publicados)[1], `"status":"poweron"`) {
		t.Errorf("el segundo evento debe ser poweron: %s", (*publicados)[1])
	}
}

// TestAlimentacionCorteObsoletoNoApaga es la guarda que más importa.
//
// Entre el aviso de apagado y el corte hay un plazo. Si el vehículo vuelve a encender
// dentro de ese plazo, el temporizador ya programado sigue en camino: sin esta guarda
// le cortaría la energía a la cámara de un vehículo en servicio, y nadie contaría hasta
// el siguiente ciclo de llave.
func TestAlimentacionCorteObsoletoNoApaga(t *testing.T) {
	a, salida, _, _ := contadorMudo(t, 1)

	a.aplicarAlimentacion(nil, suspend(false)) // se programa el corte
	a.aplicarAlimentacion(nil, suspend(true))  // el vehiculo vuelve antes del plazo
	a.cortarAlimentacion()                     // llega el temporizador viejo

	if got := nivelDe(t, salida); got != 0 {
		t.Errorf("la linea debe seguir encendida (0), el corte era obsoleto; esta en %d", got)
	}
}

// TestAlimentacionArranqueObsoletoNoRehabilita: el simétrico. Si el vehículo se apagó
// mientras la cámara arrancaba, el temporizador de arranque no debe volver a habilitar
// el diálogo con un equipo al que le vamos a quitar la energía.
func TestAlimentacionArranqueObsoletoNoRehabilita(t *testing.T) {
	a, _, programados, _ := contadorMudo(t, 1)

	a.aplicarAlimentacion(nil, suspend(false))
	a.cortarAlimentacion()
	a.aplicarAlimentacion(nil, suspend(true))  // enciende: programa msgCamaraLista
	a.aplicarAlimentacion(nil, suspend(false)) // se vuelve a apagar antes de que arranque

	antes := len(*programados)
	a.camaraTerminoDeArrancar(nil) // el temporizador viejo
	if len(*programados) != antes {
		t.Error("un arranque obsoleto no debe programar nada más")
	}
	if a.camaraEncendida {
		t.Error("el estado debe seguir siendo apagado")
	}
}

// TestAlimentacionEsIdempotente: go-ignition reemite el estado con cada evento de llave
// y al suscribirse. Sin esta guarda, cada repetición de un UP reiniciaría la espera de
// arranque y la cámara quedaría marcada como no disponible indefinidamente — contando
// cero pasajeros con el vehículo en servicio.
func TestAlimentacionEsIdempotente(t *testing.T) {
	a, _, programados, _ := contadorMudo(t, 1)

	// Arranca encendida, así que repetir el UP no debe hacer nada.
	a.aplicarAlimentacion(nil, suspend(true))
	a.aplicarAlimentacion(nil, suspend(true))
	if len(*programados) != 0 {
		t.Errorf("un UP repetido sobre un estado encendido no debe programar nada, programados=%d",
			len(*programados))
	}

	a.aplicarAlimentacion(nil, suspend(false))
	a.aplicarAlimentacion(nil, suspend(false))
	if len(*programados) != 1 {
		t.Errorf("un DOWN repetido debe programar UN corte, programados=%d", len(*programados))
	}
}

// TestAlimentacionIgnoraLaLlaveCruda: solo se actúa sobre el aviso con periodo de
// gracia. El estado crudo de la llave llega en el mismo instante y sin antirrebote;
// actuar sobre él apagaría y encendería la cámara con cada golpe de llave, y cada
// encendido cuesta dos minutos sin contar a nadie.
func TestAlimentacionIgnoraLaLlaveCruda(t *testing.T) {
	a, salida, programados, publicados := contadorMudo(t, 1)

	a.aplicarAlimentacion(nil, &MsgIgnition{Tipo: tipoIgnitionEvt, Encendido: false})
	if len(*programados) != 0 {
		t.Error("el estado crudo de la llave no debe programar un corte")
	}
	if len(*publicados) != 0 {
		t.Error("el estado crudo de la llave no debe publicar nada")
	}
	if got := nivelDe(t, salida); got != 0 {
		t.Errorf("la linea debe seguir encendida, esta en %d", got)
	}
}

// TestAlimentacionDeshabilitadaNoHaceNada: sin -cameraPowerGPIO la función no existe, y
// un evento de ignición que llegue igual no puede tener ningún efecto.
func TestAlimentacionDeshabilitadaNoHaceNada(t *testing.T) {
	a := NewCountingActor()
	mudo := log.New(ioutil.Discard, "", 0)
	a.SetLogError(mudo).SetLogWarn(mudo).SetLogInfo(mudo).SetLogBuild(mudo)
	a.initLogs()

	llamado := false
	a.programar = func(actor.Context, time.Duration, interface{}) { llamado = true }

	a.aplicarAlimentacion(nil, suspend(false))
	a.cortarAlimentacion()
	a.camaraTerminoDeArrancar(nil)

	if llamado {
		t.Error("sin GPIO configurada no se debe programar nada")
	}
	if !a.camaraEncendida {
		t.Error("sin GPIO configurada el estado debe quedar en encendida")
	}
}

// TestNivelEncendidoEsElComplemento: el nivel de encendido se deriva y no se configura
// aparte, para que no exista la combinación imposible de dos flags que se contradigan.
func TestNivelEncendidoEsElComplemento(t *testing.T) {
	for apagado, encendido := range map[int]int{0: 1, 1: 0} {
		a, salida, _, _ := contadorMudo(t, apagado)
		if got := a.nivelEncendido(); got != encendido {
			t.Errorf("con apagado=%d, nivelEncendido()=%d, quiero %d", apagado, got, encendido)
		}
		if got := nivelDe(t, salida); got != encendido {
			t.Errorf("con apagado=%d la linea arranca en %d, quiero %d", apagado, got, encendido)
		}
	}
}
