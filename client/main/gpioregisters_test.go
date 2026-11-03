package main

import "testing"

// TestBloqueGPIO fija la traducción del número de sysfs al bloque y bit del SoC.
//
// Importa más de lo que parece: equivocarse acá no produce un error, produce la lectura
// de OTRO pin, con valores perfectamente creíbles. Alguien terminaría revisando el
// hardware equivocado con un diagnóstico que dice exactamente lo contrario de la
// realidad.
//
// Las bases están verificadas contra /sys/class/gpio/gpiochipN/label en FLO-W7-0138.
func TestBloqueGPIO(t *testing.T) {
	casos := []struct {
		pin   int
		base  uintptr
		banco int
		bit   uint
	}{
		{0, 0x0209C000, 0, 0},    // primer pin del primer bloque
		{31, 0x0209C000, 0, 31},  // último del primero
		{32, 0x020A0000, 1, 0},   // primero del segundo: el borde que se equivoca solo
		{160, 0x020B0000, 5, 0},  // base de gpiochip160, medido en el equipo
		{165, 0x020B0000, 5, 5},  // el pin de la cámara: GPIO6_IO05
		{191, 0x020B0000, 5, 31}, // último del bloque de la cámara
		{192, 0x020B4000, 6, 0},  // base de gpiochip192, medido en el equipo
		{223, 0x020B4000, 6, 31}, // último pin conocido
	}
	for _, c := range casos {
		base, banco, bit, err := bloqueGPIO(c.pin)
		if err != nil {
			t.Errorf("gpio %d: no debería fallar: %s", c.pin, err)
			continue
		}
		if base != c.base {
			t.Errorf("gpio %d: base 0x%08X, quiero 0x%08X", c.pin, base, c.base)
		}
		if banco != c.banco {
			t.Errorf("gpio %d: banco %d, quiero %d", c.pin, banco, c.banco)
		}
		if bit != c.bit {
			t.Errorf("gpio %d: bit %d, quiero %d", c.pin, bit, c.bit)
		}
	}
}

// TestBloqueGPIOFueraDeRango: mejor un error que leer una dirección que no es un bloque
// de GPIO. Mapear cualquier otra cosa del mapa de periféricos y presentarla como "el
// estado de tu pin" sería peor que no tener la herramienta.
func TestBloqueGPIOFueraDeRango(t *testing.T) {
	for _, pin := range []int{-1, 224, 1000} {
		if _, _, _, err := bloqueGPIO(pin); err == nil {
			t.Errorf("gpio %d debería dar error", pin)
		}
	}
}

// TestBloqueGPIOBasesConsecutivas: los bloques del i.MX6 están cada 0x4000. Si alguien
// agrega un SoC con otro espaciado, esto obliga a revisar la tabla en vez de confiar en
// que el patrón siga.
func TestBloqueGPIOBasesConsecutivas(t *testing.T) {
	const paso = 0x4000
	for i := 1; i < len(basesGPIO); i++ {
		if d := basesGPIO[i] - basesGPIO[i-1]; d != paso {
			t.Errorf("entre el bloque %d y el %d hay 0x%X, se esperaba 0x%X",
				i-1, i, d, paso)
		}
	}
}
