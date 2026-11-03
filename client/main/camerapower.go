package main

import (
	"fmt"
	"os"

	"github.com/dumacp/go-hikvision/client"
)

// aplicarNivelGPIO es el modo utilitario de `-cameraPowerSet`: deja la línea en el nivel
// pedido y termina, sin levantar el conteo.
//
// Existe porque la polaridad del circuito no se puede adivinar desde acá y equivocarse
// es caro: si `-cameraPowerOffLevel` queda al revés, el binario arranca poniendo la
// línea en lo que cree que es "encendida", la cámara se queda sin energía, y el evento
// de MQTT informa `poweron` mientras nadie cuenta. Con esto el equipo de apoyo prueba
// los dos niveles contra el equipo real y recién después configura el flag.
func aplicarNivelGPIO(pin, nivel int) int {
	if pin <= 0 {
		fmt.Fprintln(os.Stderr, "-cameraPowerSet necesita también -cameraPowerGPIO con el número de línea")
		return 1
	}
	if nivel != 0 && nivel != 1 {
		fmt.Fprintf(os.Stderr, "-cameraPowerSet %d inválido, solo 0 o 1\n", nivel)
		return 1
	}

	salida, err := client.NuevaSalidaGPIO(pin, nivel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no se pudo manejar la gpio %d: %s\n", pin, err)
		return 1
	}

	// Se relee en vez de confiar en la escritura: sysfs la acepta y el valor puede no
	// quedar. Si el equipo de apoyo va a decidir la polaridad con esta salida, tiene
	// que ser lo que la línea tiene de verdad.
	v, err := salida.Leer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "la gpio %d no se pudo releer: %s\n", pin, err)
		return 1
	}

	fmt.Printf("gpio %d = %d\n", pin, v)
	fmt.Printf("\nMirá la cámara ahora:\n")
	fmt.Printf("  - si se APAGÓ, configurá  -cameraPowerGPIO %d -cameraPowerOffLevel %d\n", pin, v)
	fmt.Printf("  - si sigue ENCENDIDA, probá el otro nivel:  -cameraPowerGPIO %d -cameraPowerSet %d\n",
		pin, 1-v)
	fmt.Printf("\nLa línea queda en %d al salir; el binario de conteo la vuelve a tomar al arrancar.\n", v)
	return 0
}
