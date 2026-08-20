package main

import "testing"

func iguales(a, b []string) bool {
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

func TestResolveCamerasPosicional(t *testing.T) {
	// La forma posicional no cambia: es la que ya está desplegada.
	casos := []struct {
		in     []string
		quiero []string
	}{
		{nil, nil},
		{[]string{"192.168.186.91"}, []string{"192.168.186.91"}},
		{[]string{"10.0.0.1", "10.0.0.2"}, []string{"10.0.0.1", "10.0.0.2"}},
		// Hueco explícito con un valor vacío, que era el único modo de configurar solo la
		// trasera antes del sufijo ":id".
		{[]string{"", "192.168.188.21"}, []string{"", "192.168.188.21"}},
	}
	for _, c := range casos {
		got, err := resolveCameras(c.in)
		if err != nil {
			t.Errorf("resolveCameras(%v) devolvió error: %s", c.in, err)
			continue
		}
		if !iguales(got, c.quiero) {
			t.Errorf("resolveCameras(%v) = %v, quiero %v", c.in, got, c.quiero)
		}
	}
}

func TestResolveCamerasExplicita(t *testing.T) {
	// El caso que motivó el sufijo: solo la cámara trasera. Debe quedar en el índice 1,
	// igual que la clasificaba la regla histórica, y NO en el 0.
	got, err := resolveCameras([]string{"192.168.188.21:1"})
	if err != nil {
		t.Fatalf("error inesperado: %s", err)
	}
	if !iguales(got, []string{"", "192.168.188.21"}) {
		t.Fatalf("solo la trasera = %v, quiero [\"\" 192.168.188.21]", got)
	}

	// El orden en que se escriben no importa: el id manda.
	got, err = resolveCameras([]string{"10.0.0.2:1", "10.0.0.1:0"})
	if err != nil {
		t.Fatalf("error inesperado: %s", err)
	}
	if !iguales(got, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Fatalf("fuera de orden = %v, quiero [10.0.0.1 10.0.0.2]", got)
	}

	// Un id salteado deja el hueco vacío, que todo el binario ya sabe saltar.
	got, err = resolveCameras([]string{"10.0.0.3:2"})
	if err != nil {
		t.Fatalf("error inesperado: %s", err)
	}
	if !iguales(got, []string{"", "", "10.0.0.3"}) {
		t.Fatalf("id salteado = %v, quiero [\"\" \"\" 10.0.0.3]", got)
	}
}

func TestResolveCamerasErrores(t *testing.T) {
	casos := []struct {
		nombre string
		in     []string
	}{
		// Mezclar las dos formas no tiene un significado obvio, así que no se adivina.
		{"mezcla de formas", []string{"10.0.0.1", "10.0.0.2:1"}},
		{"mezcla al revés", []string{"10.0.0.1:0", "10.0.0.2"}},
		// El error que el límite existe para atrapar: un puerto TCP escrito como id.
		{"puerto en vez de id", []string{"192.168.188.21:8080"}},
		{"id negativo", []string{"192.168.188.21:-1"}},
		{"id no numérico", []string{"192.168.188.21:trasera"}},
		{"sin dirección", []string{":1"}},
		// Dos cámaras en la misma puerta: una taparía a la otra en silencio.
		{"id repetido", []string{"10.0.0.1:1", "10.0.0.2:1"}},
	}
	for _, c := range casos {
		if _, err := resolveCameras(c.in); err == nil {
			t.Errorf("%s: resolveCameras(%v) no devolvió error", c.nombre, c.in)
		}
	}
}

// TestResolveDoorBoolsCompatibilidad fija el comportamiento que hay en campo desde 1.0.25.
// Si alguno de estos casos cambia, un equipo desplegado cambia de comportamiento sin que
// nadie toque su unit file.
func TestResolveDoorBoolsCompatibilidad(t *testing.T) {
	casos := []struct {
		nombre string
		in     []string
		quiero map[int]bool
	}{
		// Un solo valor configura la puerta 0, y NO las dos. Es la forma que está
		// desplegada y la razón por la que existe la sintaxis explícita.
		{"un valor", []string{"true"}, map[int]bool{0: true}},
		{"dos valores", []string{"false", "true"}, map[int]bool{0: false, 1: true}},
		// La manga ancha histórica: cualquier cosa distinta de TRUE es false, sin avisar.
		// Se conserva a propósito en la forma posicional.
		{"case insensitive", []string{"TrUe"}, map[int]bool{0: true}},
		{"basura es false", []string{"sí"}, map[int]bool{0: false}},
		{"vacío es false", []string{""}, map[int]bool{0: false}},
		{"typo es false", []string{"tru"}, map[int]bool{0: false}},
	}
	for _, c := range casos {
		got, err := resolveDoorBools("zeroOpenState", c.in)
		if err != nil {
			t.Errorf("%s: error inesperado: %s", c.nombre, err)
			continue
		}
		if len(got) != len(c.quiero) {
			t.Errorf("%s: resolveDoorBools(%v) = %v, quiero %v", c.nombre, c.in, got, c.quiero)
			continue
		}
		for id, v := range c.quiero {
			if got[id] != v {
				t.Errorf("%s: puerta %d = %v, quiero %v", c.nombre, id, got[id], v)
			}
		}
	}
}

func TestResolveDoorBoolsExplicita(t *testing.T) {
	// El caso que motivó la sintaxis: configurar solo la puerta 1, sin relleno.
	got, err := resolveDoorBools("zeroOpenState", []string{"1=true"})
	if err != nil {
		t.Fatalf("error inesperado: %s", err)
	}
	if len(got) != 1 || !got[1] {
		t.Fatalf("solo la puerta 1 = %v, quiero {1:true}", got)
	}
	// La puerta 0 NO queda configurada: se deja el default del actor en vez de inventar
	// un false que nadie pidió.
	if _, ok := got[0]; ok {
		t.Error("la puerta 0 no debería quedar configurada")
	}

	got, err = resolveDoorBools("zeroOpenState", []string{"1=TRUE", "0=False"})
	if err != nil {
		t.Fatalf("error inesperado: %s", err)
	}
	if got[0] || !got[1] {
		t.Errorf("fuera de orden = %v, quiero {0:false, 1:true}", got)
	}
}

func TestResolveDoorBoolsErrores(t *testing.T) {
	casos := []struct {
		nombre string
		in     []string
	}{
		{"mezcla de formas", []string{"true", "1=true"}},
		{"mezcla al revés", []string{"0=true", "false"}},
		// En la forma explícita un typo es error, no un false silencioso: es sintaxis
		// nueva y no hay compatibilidad que conservar.
		{"typo en el valor", []string{"1=tru"}},
		{"valor vacío", []string{"1="}},
		{"id no numérico", []string{"trasera=true"}},
		{"id fuera de rango", []string{"99=true"}},
		{"id negativo", []string{"-1=true"}},
		{"id repetido", []string{"1=true", "1=false"}},
	}
	for _, c := range casos {
		if _, err := resolveDoorBools("zeroOpenState", c.in); err == nil {
			t.Errorf("%s: resolveDoorBools(%v) no devolvió error", c.nombre, c.in)
		}
	}
}

func TestDescribeDoorBools(t *testing.T) {
	if got := describeDoorBools(nil); got != "sin configurar (default)" {
		t.Errorf("nil = %q", got)
	}
	if got := describeDoorBools(map[int]bool{1: true}); got != "puerta 1 = true" {
		t.Errorf("una puerta = %q", got)
	}
	// El orden tiene que ser estable aunque venga de un mapa.
	quiero := "puerta 0 = false, puerta 1 = true"
	if got := describeDoorBools(map[int]bool{1: true, 0: false}); got != quiero {
		t.Errorf("dos puertas = %q, quiero %q", got, quiero)
	}
}

func TestDescribeCameras(t *testing.T) {
	casos := []struct {
		in     []string
		quiero string
	}{
		{nil, "ninguna"},
		{[]string{"", "192.168.188.21"}, "puerta 1 -> 192.168.188.21"},
		{[]string{"10.0.0.1", "10.0.0.2"}, "puerta 0 -> 10.0.0.1, puerta 1 -> 10.0.0.2"},
	}
	for _, c := range casos {
		if got := describeCameras(c.in); got != c.quiero {
			t.Errorf("describeCameras(%v) = %q, quiero %q", c.in, got, c.quiero)
		}
	}
}
