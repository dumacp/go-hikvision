package client

import "testing"

// TestEsConteoDeVentana fija las grafías REALES de las dos cámaras.
//
// La G1 con V4.2.2 escribe "TimeRange" con mayúscula y la G0 "timeRange": medido el
// mismo día en el mismo gateway, 21 eventos de una contra 16 de la otra. El filtro
// anterior comparaba sin bajar a minúsculas y solo atrapaba la segunda.
func TestEsConteoDeVentana(t *testing.T) {
	ventana := []string{
		"timeRange",     // DS-2XM6825G0
		"TimeRange",     // DS-2XM6825G1 con V4.2.2 — la que se colaba
		"timerange",     // por si aparece una tercera grafía
		"TIMERANGE",     //
		"signalTrigger", // disparo por señal: también es conteo de ventana
		"SignalTrigger",
	}
	for _, m := range ventana {
		if !esConteoDeVentana(m) {
			t.Errorf("%q debe descartarse: trae el conteo de un intervalo, no el acumulado", m)
		}
	}

	acumulado := []string{
		"realTime", // el único que trae acumulados
		"RealTime",
		"", // statisticalMethods es OPCIONAL en ISAPI: ausente se toma como realTime,
		//    porque una lista blanca descartaría todo evento de una cámara que lo omita
	}
	for _, m := range acumulado {
		if esConteoDeVentana(m) {
			t.Errorf("%q NO debe descartarse: es el acumulado de la cámara", m)
		}
	}
}
