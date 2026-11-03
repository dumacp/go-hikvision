package main

import (
	"fmt"
	"os"
	"syscall"
)

// Diagnóstico de bajo nivel de una línea de GPIO, por /dev/mem.
//
// OJO CON EL ALCANCE: esto no sabe nada de cámaras ni de Hikvision. Es una herramienta
// del GATEWAY, específica del SoC, y le serviría igual a go-ignition, go-doors o
// go-fareCollection. Vive acá porque este es el binario que maneja una GPIO y el que ya
// está desplegado en todos los equipos: obligar a copiar otro binario por SSH es
// justamente la fricción que hace que el diagnóstico no se haga. Si otro proyecto lo
// necesita, el lugar correcto es tooling compartido, no copiar este archivo.
//
// Existe porque el sysfs NO alcanza para saber si un pin está manejando. El driver de
// i.MX (gpio-mxc.c) registra el PSR como registro de lectura, así que `value` devuelve
// el nivel ELÉCTRICO del pad y no lo que pedimos. Medido en FLO-W7-0138 sobre el
// gpio165, que va a la base de un transistor:
//
//	GDIR bit 5 = 1   es salida
//	DR   bit 5 = 1   el SoC maneja ALTO
//	PSR  bit 5 = 0   el pad lee BAJO  ← la unión base-emisor clampea a ~0.7 V
//
// Comparar DR contra PSR es lo único que separa "el software no hizo su parte" de "el
// software hizo su parte y el problema está en el hardware".

// basesGPIO son los siete bloques del i.MX6. Verificados contra las etiquetas que
// reporta /sys/class/gpio/gpiochipN/label en el equipo.
var basesGPIO = []uintptr{
	0x0209C000, // GPIO1, sysfs 0..31
	0x020A0000, // GPIO2, sysfs 32..63
	0x020A4000, // GPIO3, sysfs 64..95
	0x020A8000, // GPIO4, sysfs 96..127
	0x020AC000, // GPIO5, sysfs 128..159
	0x020B0000, // GPIO6, sysfs 160..191
	0x020B4000, // GPIO7, sysfs 192..223
}

// Desplazamientos dentro del bloque.
const (
	regDR   = 0x00 // lo que PEDIMOS manejar
	regGDIR = 0x04 // dirección: 1 = salida
	regPSR  = 0x08 // lo que el pad VE de verdad
)

// bloqueGPIO traduce el número de sysfs al bloque y bit del SoC.
//
// Se separa del resto para poder probarlo: equivocarse acá no da un error, da la
// lectura de OTRO pin, y alguien terminaría revisando el hardware equivocado.
func bloqueGPIO(pin int) (base uintptr, banco int, bit uint, err error) {
	if pin < 0 {
		return 0, 0, 0, fmt.Errorf("gpio %d: no puede ser negativo", pin)
	}
	banco, bit = pin/32, uint(pin%32)
	if banco >= len(basesGPIO) {
		return 0, 0, 0, fmt.Errorf("gpio %d: cae fuera de los %d bloques conocidos del i.MX6",
			pin, len(basesGPIO))
	}
	return basesGPIO[banco], banco, bit, nil
}

// mostrarRegistrosGPIO es el modo utilitario de `-gpioRegisters`. Solo lee.
func mostrarRegistrosGPIO(pin int) int {
	base, banco, bit, err := bloqueGPIO(pin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	f, err := os.OpenFile("/dev/mem", os.O_RDONLY|syscall.O_SYNC, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no se pudo abrir /dev/mem: %s\n", err)
		return 1
	}
	defer f.Close()

	// El bloque entero entra en una página; se mapea la que lo contiene. Se usa mmap y
	// no read porque en ARM las regiones de periféricos no están en el mapa lineal del
	// kernel: un `dd if=/dev/mem` falla con "Bad address" aunque CONFIG_STRICT_DEVMEM
	// esté desactivado.
	pagina := syscall.Getpagesize()
	inicio := base &^ uintptr(pagina-1)
	desplazamiento := int(base - inicio)

	mem, err := syscall.Mmap(int(f.Fd()), int64(inicio), pagina, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no se pudo mapear 0x%08X: %s\n", inicio, err)
		return 1
	}
	defer syscall.Munmap(mem)

	leer := func(off int) uint32 {
		p := desplazamiento + off
		return uint32(mem[p]) | uint32(mem[p+1])<<8 | uint32(mem[p+2])<<16 | uint32(mem[p+3])<<24
	}
	dr, gdir, psr := leer(regDR), leer(regGDIR), leer(regPSR)
	b := func(v uint32) int { return int((v >> bit) & 1) }

	fmt.Printf("gpio %d  =  GPIO%d_IO%02d   (bloque en 0x%08X, bit %d)\n\n",
		pin, banco+1, bit, base, bit)
	fmt.Printf("  GDIR   0x%08X   bit %-2d = %d   %s\n", gdir, bit, b(gdir), direccionGPIO(b(gdir)))
	fmt.Printf("  DR     0x%08X   bit %-2d = %d   nivel que PEDIMOS manejar\n", dr, bit, b(dr))
	fmt.Printf("  PSR    0x%08X   bit %-2d = %d   nivel que el pad VE\n", psr, bit, b(psr))
	fmt.Println()

	switch {
	case b(gdir) == 0:
		fmt.Println("El pin está como ENTRADA: el SoC no maneja nada acá.")
	case b(dr) == b(psr):
		fmt.Println("DR y PSR coinciden: el pin maneja y el pad lo confirma.")
	default:
		fmt.Printf("DR dice %d y el pad lee %d.\n\n", b(dr), b(psr))
		fmt.Println("El SoC SÍ está manejando el pin; lo que no coincide es la lectura.")
		fmt.Println("Con la GPIO en la base de un transistor eso es lo ESPERADO: la unión")
		fmt.Println("base-emisor clampea a ~0.7 V y un pad de 3.3 V lo lee como nivel bajo.")
		fmt.Println("También daría esto un pad sin el camino de entrada habilitado (SION).")
		fmt.Println()
		fmt.Println("En cualquiera de los dos casos el problema NO está en el software: el")
		fmt.Println("registro de datos tiene lo que le pedimos. Lo resuelve un multímetro")
		fmt.Println("en la base del transistor.")
	}
	return 0
}

func direccionGPIO(v int) string {
	if v == 1 {
		return "SALIDA"
	}
	return "entrada"
}
