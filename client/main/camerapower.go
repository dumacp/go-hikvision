package main

import (
	"errors"
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
	noVerificado := errors.Is(err, client.ErrNivelNoVerificado)
	if err != nil && !noVerificado {
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

	fmt.Printf("gpio %d: se pidió %d y lee %d\n", pin, nivel, v)
	if noVerificado {
		fmt.Printf("\nOJO: la escritura se hizo pero la línea lee %d.\n", v)
		fmt.Printf("En este hardware (i.MX) el `value` de una salida lee el nivel ELÉCTRICO\n")
		fmt.Printf("del pad, no lo que pedimos. Con el relé sin cablear lee 0 siempre, así que\n")
		fmt.Printf("esto es lo ESPERADO hasta que el pin esté conectado. Si ya está conectado y\n")
		fmt.Printf("sigue leyendo %d, el pin no está muxeado como GPIO y hay que ver el hardware.\n", v)
	}
	// Se recomienda el nivel que se ESCRIBIÓ, no el que se lee: lo que hace reaccionar
	// al relé es lo que manejamos, y con el pad sin cablear la lectura no lo refleja.
	fmt.Printf("\nMirá la cámara ahora:\n")
	fmt.Printf("  - si se APAGÓ, configurá  -cameraPowerGPIO %d -cameraPowerOffLevel %d\n", pin, nivel)
	fmt.Printf("  - si sigue ENCENDIDA, probá el otro nivel:  -cameraPowerGPIO %d -cameraPowerSet %d\n",
		pin, 1-nivel)
	fmt.Printf("\nSe dejó la línea manejada en %d; el binario de conteo la vuelve a tomar al arrancar.\n", nivel)
	return 0
}
