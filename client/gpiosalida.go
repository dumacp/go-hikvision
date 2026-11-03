package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// rutaSysfsGPIO es la raíz estándar de sysfs para GPIO.
const rutaSysfsGPIO = "/sys/class/gpio"

// esperaExport es cuánto se espera a que aparezcan los archivos del pin después de
// exportarlo. El kernel los crea enseguida, pero los permisos los ajusta udev un
// instante después: abrir `direction` demasiado pronto da EACCES. El script del propio
// equipo (/usr/sbin/omvz7/switch.sh) resuelve esto con un `sleep 1` a ciegas; acá se
// reintenta hasta que se pueda, que en el caso normal termina en el primer intento.
const esperaExport = 2 * time.Second

// SalidaGPIO es una línea de GPIO configurada como salida, manejada por sysfs.
//
// Se escribe a mano y NO con github.com/brian-armstrong/gpio, que ya está en go.mod:
// esa librería llama a os.Exit(1) en siete lugares distintos ante cualquier fallo al
// abrir un archivo de sysfs. Un pin mal escrito en la línea de comandos mataría el
// proceso de conteo entero —sin panic, sin log, y sin que la supervisión pudiera
// distinguirlo de una salida limpia—, que es un precio absurdo por un error de
// configuración. Acá todo error se devuelve y quien llama decide.
//
// El procedimiento es el mismo que usa el equipo en /usr/sbin/omvz7/switch.sh:
//
//	echo N   > /sys/class/gpio/export
//	echo out > /sys/class/gpio/gpioN/direction
//	echo 1   > /sys/class/gpio/gpioN/value
type SalidaGPIO struct {
	raiz string
	pin  int
}

// NuevaSalidaGPIO exporta el pin, lo deja como salida y lo pone en nivelInicial.
//
// La dirección se fija escribiendo "low" o "high" —no "out"— porque sysfs los trata
// como "salida con este valor inicial" en una sola operación atómica. Escribir "out" y
// después el valor deja el pin un instante en el nivel que tuviera antes, y ese
// parpadeo sobre un relé de alimentación es justo lo que no queremos.
func NuevaSalidaGPIO(pin, nivelInicial int) (*SalidaGPIO, error) {
	return nuevaSalidaGPIOEn(rutaSysfsGPIO, pin, nivelInicial)
}

// nuevaSalidaGPIOEn existe para poder probar contra un directorio temporal: el equipo
// de desarrollo no tiene /sys/class/gpio.
func nuevaSalidaGPIOEn(raiz string, pin, nivelInicial int) (*SalidaGPIO, error) {
	if pin < 0 {
		return nil, fmt.Errorf("gpio %d: el número de pin no puede ser negativo", pin)
	}
	if nivelInicial != 0 && nivelInicial != 1 {
		return nil, fmt.Errorf("gpio %d: nivel %d inválido, solo 0 o 1", pin, nivelInicial)
	}

	s := &SalidaGPIO{raiz: raiz, pin: pin}

	// Exportar puede fallar con EBUSY porque el pin ya esté exportado —por un arranque
	// anterior, o por otro programa del equipo— y eso NO es un error: lo que importa es
	// que el directorio exista después. Por eso el fallo del export solo se reporta si
	// además el directorio no aparece.
	errExport := escribirArchivo(filepath.Join(raiz, "export"), strconv.Itoa(pin))

	dir := s.dirPin()
	if err := esperarDirectorio(dir, esperaExport); err != nil {
		if errExport != nil {
			return nil, fmt.Errorf("gpio %d: no se pudo exportar (%w) y %q no existe", pin, errExport, dir)
		}
		return nil, fmt.Errorf("gpio %d: %w", pin, err)
	}

	direccion := "low"
	if nivelInicial == 1 {
		direccion = "high"
	}
	if err := escribirConReintento(filepath.Join(dir, "direction"), direccion, esperaExport); err != nil {
		return nil, fmt.Errorf("gpio %d: no se pudo poner como salida: %w", pin, err)
	}

	// Releer: sysfs acepta la escritura y después puede no haber quedado, igual que
	// pasa con el perfil de codificación de la cámara. Si no quedó, mejor fallar ahora
	// que creer que controlamos una línea que no controlamos.
	if v, err := s.Leer(); err != nil {
		return nil, fmt.Errorf("gpio %d: no se pudo verificar el nivel inicial: %w", pin, err)
	} else if v != nivelInicial {
		return nil, fmt.Errorf("gpio %d: se pidió nivel %d y quedó en %d", pin, nivelInicial, v)
	}

	return s, nil
}

// Pin devuelve el número de línea, para los logs.
func (s *SalidaGPIO) Pin() int { return s.pin }

func (s *SalidaGPIO) dirPin() string {
	return filepath.Join(s.raiz, fmt.Sprintf("gpio%d", s.pin))
}

// Escribir pone la línea en nivel (0 o 1).
func (s *SalidaGPIO) Escribir(nivel int) error {
	if nivel != 0 && nivel != 1 {
		return fmt.Errorf("gpio %d: nivel %d inválido, solo 0 o 1", s.pin, nivel)
	}
	if err := escribirArchivo(filepath.Join(s.dirPin(), "value"), strconv.Itoa(nivel)); err != nil {
		return fmt.Errorf("gpio %d: %w", s.pin, err)
	}
	return nil
}

// Leer devuelve el nivel actual de la línea.
func (s *SalidaGPIO) Leer() (int, error) {
	datos, err := os.ReadFile(filepath.Join(s.dirPin(), "value"))
	if err != nil {
		return 0, fmt.Errorf("gpio %d: %w", s.pin, err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(datos)))
	if err != nil {
		return 0, fmt.Errorf("gpio %d: valor ilegible %q: %w", s.pin, datos, err)
	}
	return v, nil
}

func escribirArchivo(ruta, contenido string) error {
	f, err := os.OpenFile(ruta, os.O_WRONLY, 0o200)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(contenido)
	return err
}

func escribirConReintento(ruta, contenido string, tope time.Duration) error {
	limite := time.Now().Add(tope)
	var err error
	for {
		if err = escribirArchivo(ruta, contenido); err == nil {
			return nil
		}
		if time.Now().After(limite) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func esperarDirectorio(ruta string, tope time.Duration) error {
	limite := time.Now().Add(tope)
	for {
		if fi, err := os.Stat(ruta); err == nil && fi.IsDir() {
			return nil
		}
		if time.Now().After(limite) {
			return fmt.Errorf("%q no apareció tras %s", ruta, tope)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
