package isapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// cot es UTC−5 sin horario de verano, la zona de los equipos en campo.
var cot = time.FixedZone("COT", -5*3600)

// camaraGrabando simula la búsqueda de grabaciones de una cámara que grabó de corrido
// [desde, hasta] (instantes reales), partido en archivos de 5 minutos como la G1, y cuyo
// índice solo llega hasta hasta: para simular el atraso del índice basta con pasar un
// hasta anterior al final de la ventana.
//
// relojDePared reproduce lo medido en la DS-2XM6825G1 con V4.2.2: lee los instantes de la
// petición como su hora local aunque traigan `Z`, y responde los tramos igual. Sin él se
// comporta como la G0, que usa UTC de verdad. Cuenta las búsquedas en *n.
func camaraGrabando(t *testing.T, desde, hasta time.Time, relojDePared bool, n *int) func(http.ResponseWriter, *http.Request) {
	leer := func(s string) time.Time {
		loc := time.UTC
		if relojDePared {
			loc = cot
		}
		v, err := time.ParseInLocation(searchTimeLayout, s, loc)
		if err != nil {
			t.Fatalf("instante inválido %q: %v", s, err)
		}
		return v
	}
	escribir := func(v time.Time) string {
		if relojDePared {
			return v.In(cot).Format(searchTimeLayout)
		}
		return v.UTC().Format(searchTimeLayout)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		*n++
		body, _ := io.ReadAll(r.Body)
		var req CMSearchDescription
		if err := xml.Unmarshal(body, &req); err != nil || len(req.TimeSpans) != 1 {
			t.Fatalf("petición inválida: %v\n%s", err, body)
		}
		from, to := leer(req.TimeSpans[0].StartTime), leer(req.TimeSpans[0].EndTime)
		var items strings.Builder
		k := 0
		for ini := desde; ini.Before(hasta); ini = ini.Add(5 * time.Minute) {
			fin := ini.Add(5 * time.Minute)
			if fin.After(hasta) {
				fin = hasta
			}
			if fin.Before(from) || ini.After(to) {
				continue
			}
			k++
			fmt.Fprintf(&items, `<searchMatchItem><trackID>101</trackID><timeSpan>`+
				`<startTime>%s</startTime><endTime>%s</endTime></timeSpan></searchMatchItem>`,
				escribir(ini), escribir(fin))
		}
		w.Header().Set("Content-Type", "application/xml")
		if k == 0 {
			fmt.Fprint(w, `<CMSearchResult><responseStatus>true</responseStatus>`+
				`<responseStatusStrg>NO MATCHES</responseStatusStrg><numOfMatches>0</numOfMatches></CMSearchResult>`)
			return
		}
		fmt.Fprintf(w, `<CMSearchResult><responseStatus>true</responseStatus>`+
			`<responseStatusStrg>OK</responseStatusStrg><numOfMatches>%d</numOfMatches>`+
			`<matchList>%s</matchList></CMSearchResult>`, k, items.String())
	}
}

// ventana es la de un evento cualquiera: 16 s que caen dentro de lo grabado.
func ventana() (grabDesde, grabHasta, from, to time.Time) {
	grabDesde = time.Date(2026, 11, 7, 8, 58, 59, 0, cot)
	grabHasta = grabDesde.Add(5 * time.Minute)
	from = grabDesde.Add(time.Minute)
	return grabDesde, grabHasta, from, from.Add(16 * time.Second)
}

func TestHasCoverageEncuentraLaG1EnHoraLocal(t *testing.T) {
	gd, gh, from, to := ventana()
	var n int
	srv := digestServer(t, camaraGrabando(t, gd, gh, true, &n))
	defer srv.Close()

	cov, err := clientFor(srv).HasCoverage(context.Background(), 101, from, to, "id", false)
	if err != nil || cov.Tramo == nil {
		t.Fatalf("la G1 tiene ese tramo grabado y no se encontró: %+v err=%v", cov, err)
	}
	if !cov.HoraLocal {
		t.Error("debía informar que la convención que funcionó es la hora local")
	}
	if n != 2 {
		t.Errorf("sin preferencia van dos búsquedas (UTC y local), hubo %d", n)
	}

	// Con la convención ya aprendida, una sola búsqueda.
	n = 0
	if cov, err = clientFor(srv).HasCoverage(context.Background(), 101, from, to, "id", true); cov.Tramo == nil || !cov.HoraLocal || err != nil {
		t.Fatalf("con preferLocal debía encontrarlo: %+v err=%v", cov, err)
	}
	if n != 1 {
		t.Errorf("con la convención aprendida debía bastar una búsqueda, hubo %d", n)
	}
}

func TestHasCoverageLaG0SigueEnUTCYEnUnIntento(t *testing.T) {
	gd, gh, from, to := ventana()
	var n int
	srv := digestServer(t, camaraGrabando(t, gd, gh, false, &n))
	defer srv.Close()

	cov, err := clientFor(srv).HasCoverage(context.Background(), 101, from, to, "id", false)
	if err != nil || cov.Tramo == nil || cov.HoraLocal {
		t.Fatalf("la G0 se resuelve en UTC: %+v err=%v", cov, err)
	}
	if n != 1 {
		t.Errorf("una cámara en UTC no debe pagar la búsqueda extra, hubo %d", n)
	}

	// Si la preferencia quedó mal aprendida, se corrige sola.
	if cov, err = clientFor(srv).HasCoverage(context.Background(), 101, from, to, "id", true); cov.Tramo == nil || cov.HoraLocal || err != nil {
		t.Fatalf("una preferencia equivocada debía corregirse: %+v err=%v", cov, err)
	}
}

func TestHasCoverageSinGrabacionNoEsError(t *testing.T) {
	gd, gh, _, _ := ventana()
	from := gh.Add(time.Hour) // fuera de lo grabado en cualquier convención
	for _, pared := range []bool{false, true} {
		var n int
		srv := digestServer(t, camaraGrabando(t, gd, gh, pared, &n))
		cov, err := clientFor(srv).HasCoverage(context.Background(), 101, from, from.Add(16*time.Second), "id", false)
		srv.Close()
		if cov.Tramo != nil || cov.Pendiente || err != nil {
			t.Errorf("relojDePared=%v: sin grabación debe dar vacío y sin error, dio %+v err=%v", pared, cov, err)
		}
	}
}

func TestCoversReinterpretaSoloLaZ(t *testing.T) {
	_, _, from, to := ventana()
	pared := Recording{StartTime: "2026-11-07T08:58:59Z", EndTime: "2026-11-07T09:03:59Z"}
	if pared.Covers(from, to) {
		t.Error("leído como UTC ese tramo está 5 h antes y no puede cubrir la ventana")
	}
	if !pared.covers(from, to, true) {
		t.Error("leído como reloj de pared el tramo cubre la ventana")
	}
	explicito := Recording{StartTime: "2026-11-07T08:58:59-05:00", EndTime: "2026-11-07T09:03:59-05:00"}
	if !explicito.covers(from, to, true) || !explicito.Covers(from, to) {
		t.Error("un desfase explícito dice lo que es y no se reinterpreta")
	}
}

func TestHasCoverageUneLosArchivosDeCincoMinutos(t *testing.T) {
	gd, _, _, _ := ventana()
	// Una ventana que cruza el corte entre el primer y el segundo archivo.
	from := gd.Add(5*time.Minute - 8*time.Second)
	to := from.Add(16 * time.Second)
	var n int
	srv := digestServer(t, camaraGrabando(t, gd, gd.Add(15*time.Minute), true, &n))
	defer srv.Close()

	cov, err := clientFor(srv).HasCoverage(context.Background(), 101, from, to, "id", true)
	if err != nil || cov.Tramo == nil {
		t.Fatalf("la ventana está grabada aunque cruce el corte de archivo: %+v err=%v", cov, err)
	}
}

func TestHasCoverageIndiceAtrasadoEsPendiente(t *testing.T) {
	gd, _, from, to := ventana()
	for _, c := range []struct {
		nombre    string
		indexado  time.Time
		pendiente bool
	}{
		// Lo medido: el índice terminaba 1 min antes de la ventana.
		{"el índice no llega a la ventana", from.Add(-time.Minute), true},
		{"el índice corta en medio de la ventana", from.Add(5 * time.Second), true},
		// La cámara dejó de grabar mucho antes: no hay nada que esperar.
		{"la grabación terminó hace rato", from.Add(-BusquedaAtras - time.Minute), false},
	} {
		var n int
		srv := digestServer(t, camaraGrabando(t, gd.Add(-10*time.Minute), c.indexado, true, &n))
		cov, err := clientFor(srv).HasCoverage(context.Background(), 101, from, to, "id", true)
		srv.Close()
		if err != nil || cov.Tramo != nil {
			t.Fatalf("%s: no debía haber tramo completo: %+v err=%v", c.nombre, cov, err)
		}
		if cov.Pendiente != c.pendiente {
			t.Errorf("%s: Pendiente=%v, se esperaba %v", c.nombre, cov.Pendiente, c.pendiente)
		}
		if c.pendiente && !cov.IndexadoHasta.Equal(c.indexado) {
			t.Errorf("%s: IndexadoHasta=%v, se esperaba %v", c.nombre, cov.IndexadoHasta, c.indexado)
		}
	}
}

func TestHasCoverageDiceQueArchivosBajar(t *testing.T) {
	gd, _, _, _ := ventana()
	var n int
	srv := digestServer(t, camaraGrabando(t, gd, gd.Add(15*time.Minute), true, &n))
	defer srv.Close()

	// Dentro de un solo archivo.
	from := gd.Add(time.Minute)
	cov, err := clientFor(srv).HasCoverage(context.Background(), 101, from, from.Add(16*time.Second), "id", true)
	if err != nil || len(cov.Archivos) != 1 || !cov.Archivos[0].Desde.Equal(gd) {
		t.Fatalf("debía ser el primer archivo, que empieza en %v: %+v err=%v", gd, cov.Archivos, err)
	}
	// Cruzando el corte: los dos, en orden.
	from = gd.Add(5*time.Minute - 8*time.Second)
	cov, err = clientFor(srv).HasCoverage(context.Background(), 101, from, from.Add(16*time.Second), "id", true)
	if err != nil || len(cov.Archivos) != 2 || !cov.Archivos[1].Desde.Equal(gd.Add(5*time.Minute)) {
		t.Fatalf("debían ser los dos archivos del corte: %+v err=%v", cov.Archivos, err)
	}
}
