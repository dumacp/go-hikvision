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
