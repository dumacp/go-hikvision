package parametros

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const jsonBueno = `{"cameras":[{"ip":"192.168.186.93","door":0,"zeroOpenState":true}],
  "videoFrameRate":12,"videoEnabled":true}`

// TestCacheIdaYVuelta. El cache existe porque el de go-params no funciona —escribe un
// esquema y lee otro, y nunca propaga lo leído— y sin él un arranque sin red deja el equipo
// con los defaults. Con la prioridad invertida de este binario eso es peligroso: un lanzador
// que delega la configuración no trae los flags, y el default de zeroOpenState descarta
// todos los cruces en un vehículo cableado al revés.
func TestCacheIdaYVuelta(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "sub", "cameracount-config.json")

	if err := guardarCache(ruta, []byte(jsonBueno)); err != nil {
		t.Fatalf("guardar: %s", err)
	}
	cfg, err := LeerCache(ruta)
	if err != nil {
		t.Fatalf("leer: %s", err)
	}
	if cfg == nil {
		t.Fatal("se guardó una configuración y se leyó nil")
	}
	if cfg.VideoFrameRate == nil || *cfg.VideoFrameRate != 12 {
		t.Error("no sobrevivió videoFrameRate")
	}
	if len(cfg.Camaras) != 1 || cfg.Camaras[0].IP != "192.168.186.93" {
		t.Errorf("no sobrevivieron las cámaras: %+v", cfg.Camaras)
	}
}

// TestCacheNoDejaArchivoAMedias: se escribe como .part y se renombra, y el rename es
// atómico. Un corte de energía en mitad de la escritura tiene que dejar el cache anterior
// intacto, no uno truncado — que sería ilegible justo en el arranque siguiente, que es
// cuando más falta hace.
func TestCacheNoDejaArchivoAMedias(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, "config.json")

	if err := guardarCache(ruta, []byte(jsonBueno)); err != nil {
		t.Fatalf("guardar: %s", err)
	}
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entradas {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("quedó el temporal %q sin renombrar", e.Name())
		}
	}
	if len(entradas) != 1 {
		t.Errorf("se esperaba un solo archivo, hay %d", len(entradas))
	}
}

// TestCacheAusenteNoEsError: no tener cache es lo normal el primer día de un equipo.
func TestCacheAusenteNoEsError(t *testing.T) {
	cfg, err := LeerCache(filepath.Join(t.TempDir(), "no-existe.json"))
	if err != nil {
		t.Errorf("no debería dar error: %s", err)
	}
	if cfg != nil {
		t.Error("no hay cache, debería devolver nil")
	}
}

// TestCacheDeshabilitado: ruta vacía es "sin cache", no un error ni un archivo con nombre
// raro en el directorio de trabajo.
func TestCacheDeshabilitado(t *testing.T) {
	if err := guardarCache("", []byte(jsonBueno)); err != nil {
		t.Errorf("guardar con ruta vacía: %s", err)
	}
	cfg, err := LeerCache("")
	if err != nil || cfg != nil {
		t.Errorf("leer con ruta vacía: cfg=%v err=%v", cfg, err)
	}
}

// TestCacheCorruptoSeReporta. No se puede aplicar a medias ni en silencio: quien llama tiene
// que poder decir en el log que el equipo arrancó con los defaults y por qué.
func TestCacheCorruptoSeReporta(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(ruta, []byte(`{"videoFrameRate":`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LeerCache(ruta)
	if err == nil {
		t.Fatal("un cache corrupto tiene que dar error")
	}
	if cfg != nil {
		t.Error("con error no se devuelve configuración")
	}
	if !strings.Contains(err.Error(), ruta) {
		t.Errorf("el error no nombra el archivo: %s", err)
	}
}

// TestCacheRechazaUnaConfiguracionInvalida: el archivo puede ser JSON válido y aun así
// describir algo que no se puede aplicar. Validar al leer evita arrancar con una
// configuración que después se rechaza a medias.
func TestCacheRechazaUnaConfiguracionInvalida(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(ruta, []byte(`{"cameraPowerOffLevel":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LeerCache(ruta); err == nil {
		t.Fatal("cameraPowerOffLevel 7 tenía que rechazarse")
	}
}
