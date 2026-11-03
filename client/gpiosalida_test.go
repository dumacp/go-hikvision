package client

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sysfsFalso arma un /sys/class/gpio de mentira con el pin ya exportado y su `value` en
// nivelActual. No puede emular que escribir "high" en `direction` mueva `value` —eso lo
// hace el kernel—, así que `value` se siembra y las pruebas se escriben sabiéndolo.
func sysfsFalso(t *testing.T, pin, nivelActual int) string {
	t.Helper()
	raiz := t.TempDir()
	if err := os.WriteFile(filepath.Join(raiz, "export"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(raiz, "gpio"+strconv.Itoa(pin))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for nombre, contenido := range map[string]string{
		"direction": "in\n",
		"value":     strconv.Itoa(nivelActual) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, nombre), []byte(contenido), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return raiz
}

func leerArchivo(t *testing.T, partes ...string) string {
	t.Helper()
	datos, err := os.ReadFile(filepath.Join(partes...))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(datos))
}

// TestSalidaGPIOEscribeLaSecuenciaDeSysfs: el pin se exporta y la dirección se fija con
// "low"/"high", no con "out". Es deliberado: sysfs lo trata como "salida con este valor
// inicial" en una sola operación, mientras que "out" seguido del valor deja la línea un
// instante en el nivel anterior, y ese parpadeo sobre un relé de alimentación apagaría
// la cámara por un momento al arrancar el binario.
func TestSalidaGPIOEscribeLaSecuenciaDeSysfs(t *testing.T) {
	for _, c := range []struct {
		nivel     int
		direccion string
	}{
		{0, "low"},
		{1, "high"},
	} {
		raiz := sysfsFalso(t, 164, c.nivel)
		s, err := nuevaSalidaGPIOEn(raiz, 164, c.nivel)
		if err != nil {
			t.Fatalf("nivel %d: no debería fallar: %s", c.nivel, err)
		}
		if got := leerArchivo(t, raiz, "export"); got != "164" {
			t.Errorf("export = %q, quiero \"164\"", got)
		}
		if got := leerArchivo(t, raiz, "gpio164", "direction"); got != c.direccion {
			t.Errorf("direction = %q, quiero %q", got, c.direccion)
		}
		if s.Pin() != 164 {
			t.Errorf("Pin() = %d", s.Pin())
		}
	}
}

// TestSalidaGPIOEscribirYLeer: el ciclo normal de uso.
func TestSalidaGPIOEscribirYLeer(t *testing.T) {
	raiz := sysfsFalso(t, 164, 0)
	s, err := nuevaSalidaGPIOEn(raiz, 164, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Escribir(1); err != nil {
		t.Fatalf("Escribir(1): %s", err)
	}
	if got := leerArchivo(t, raiz, "gpio164", "value"); got != "1" {
		t.Errorf("value = %q, quiero \"1\"", got)
	}
	v, err := s.Leer()
	if err != nil {
		t.Fatalf("Leer: %s", err)
	}
	if v != 1 {
		t.Errorf("Leer() = %d, quiero 1", v)
	}
}

// TestSalidaGPIOVerificaQueElNivelQuedo es la guarda que importa: sysfs acepta la
// escritura y el valor puede no quedar. Si creyéramos que controlamos una línea que no
// controlamos, apagaríamos la cámara "con éxito" sin apagarla, o peor, creeríamos
// haberla encendido y el conteo quedaría muerto sin que nada fallara a la vista.
func TestSalidaGPIOVerificaQueElNivelQuedo(t *testing.T) {
	// Se pide nivel 1 pero el `value` del sysfs falso quedó en 0.
	raiz := sysfsFalso(t, 164, 0)
	if _, err := nuevaSalidaGPIOEn(raiz, 164, 1); err == nil {
		t.Fatal("si el nivel no quedó, tiene que dar error")
	} else if !strings.Contains(err.Error(), "quedó en 0") {
		t.Errorf("el error debe decir qué quedó: %s", err)
	}
}

// TestSalidaGPIOArgumentosInvalidos: estos fallan sin tocar el filesystem, así que no
// cuestan el tiempo de espera del export.
func TestSalidaGPIOArgumentosInvalidos(t *testing.T) {
	if _, err := nuevaSalidaGPIOEn(t.TempDir(), -1, 0); err == nil {
		t.Error("un pin negativo debe dar error")
	}
	if _, err := nuevaSalidaGPIOEn(t.TempDir(), 164, 7); err == nil {
		t.Error("un nivel distinto de 0 y 1 debe dar error")
	}
	raiz := sysfsFalso(t, 164, 0)
	s, err := nuevaSalidaGPIOEn(raiz, 164, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Escribir(7); err == nil {
		t.Error("Escribir con un nivel inválido debe dar error")
	}
}

// TestSalidaGPIOPinYaExportadoNoEsError cubre un caso REAL del equipo: el script
// /usr/sbin/omvz7/switch.sh exporta el gpio165 a mano y lo deja exportado. Si alguien
// lo corre y después arranca el binario, el `echo 165 > export` falla con EBUSY. Eso no
// es un error: lo que importa es que el directorio del pin exista y se pueda escribir.
// Tratarlo como fallo dejaría al binario sin control de la cámara por un script que
// alguien corrió antes.
func TestSalidaGPIOPinYaExportadoNoEsError(t *testing.T) {
	raiz := sysfsFalso(t, 165, 0)
	// Un `export` de solo lectura hace fallar la escritura, como el EBUSY del kernel
	// cuando el pin ya está tomado.
	if err := os.Chmod(filepath.Join(raiz, "export"), 0o400); err != nil {
		t.Fatal(err)
	}
	s, err := nuevaSalidaGPIOEn(raiz, 165, 0)
	if err != nil {
		t.Fatalf("con el pin ya exportado no debe fallar: %s", err)
	}
	if got := leerArchivo(t, raiz, "gpio165", "direction"); got != "low" {
		t.Errorf("direction = %q, quiero \"low\"", got)
	}
	if err := s.Escribir(1); err != nil {
		t.Errorf("y debe quedar usable: %s", err)
	}
}

// TestSalidaGPIOPinInexistenteNoMataElProceso es la razón de no usar
// github.com/brian-armstrong/gpio, que ya está en go.mod: esa librería llama a
// os.Exit(1) ante este mismo caso. Si esta prueba pasa, es porque el proceso sigue vivo
// para contarlo — un pin mal escrito en la línea de comandos no puede tumbar el conteo
// de un vehículo.
func TestSalidaGPIOPinInexistenteNoMataElProceso(t *testing.T) {
	raiz := t.TempDir() // sin `export` y sin gpioN/
	s, err := nuevaSalidaGPIOEn(raiz, 999, 0)
	if err == nil {
		t.Fatal("un pin que no se puede exportar debe dar error")
	}
	if s != nil {
		t.Error("con error no se devuelve una salida usable")
	}
	if !strings.Contains(err.Error(), "999") {
		t.Errorf("el error debe nombrar el pin: %s", err)
	}
}
