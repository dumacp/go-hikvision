package isapi

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testUser  = "admin"
	testPass  = "Cl4v3-d3-Pru3ba"
	testRealm = "IP Camera(K9754)"
	testNonce = "4e5755795a6d4d344e3255365a6a55324e5455354e44413d"
)

// digestServer answers like the camera: 401 with a Digest challenge, then validates the
// Authorization header by recomputing the expected response. A wrong implementation on
// the client side gets a second 401, which surfaces as ErrUnauthorized.
func digestServer(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(
				`Digest qop="auth", realm=%q, nonce=%q, stale="FALSE"`, testRealm, testNonce))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := validateDigest(auth, r.Method); err != nil {
			t.Errorf("Authorization inválida: %v (header: %s)", err, auth)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reply(w, r)
	}))
}

func validateDigest(header, method string) error {
	fields := map[string]string{}
	for _, f := range splitChallenge(strings.TrimPrefix(header, "Digest ")) {
		kv := strings.SplitN(strings.TrimSpace(f), "=", 2)
		if len(kv) == 2 {
			fields[kv[0]] = strings.Trim(kv[1], `"`)
		}
	}
	for _, k := range []string{"username", "realm", "nonce", "uri", "cnonce", "nc", "qop", "response"} {
		if fields[k] == "" {
			return fmt.Errorf("falta el campo %q", k)
		}
	}
	if fields["realm"] != testRealm || fields["nonce"] != testNonce {
		return errors.New("realm o nonce alterados")
	}
	if fields["qop"] != "auth" {
		return fmt.Errorf("qop inesperado %q", fields["qop"])
	}
	ha1 := md5hex(testUser, testRealm, testPass)
	ha2 := md5hex(method, fields["uri"])
	want := md5hex(ha1, testNonce, fields["nc"], fields["cnonce"], "auth", ha2)
	if fields["response"] != want {
		return fmt.Errorf("response %s, esperaba %s", fields["response"], want)
	}
	return nil
}

func clientFor(srv *httptest.Server) *Client {
	return New(strings.TrimPrefix(srv.URL, "http://"), testUser, testPass, 5*time.Second)
}

func TestGetTimeConDigest(t *testing.T) {
	srv := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", `application/xml; charset="UTF-8"`)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<Time version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
<timeMode>NTP</timeMode>
<localTime>2026-08-12T13:07:06-05:00</localTime>
<timeZone>CST+5:00:00</timeZone>
<satelliteInterval>1440</satelliteInterval>
</Time>`)
	})
	defer srv.Close()

	got, err := clientFor(srv).GetTime(context.Background())
	if err != nil {
		t.Fatalf("GetTime: %v", err)
	}
	if got.TimeMode != "NTP" || got.TimeZone != "CST+5:00:00" {
		t.Errorf("campos mal parseados: %+v", got)
	}
	if _, err := got.Parsed(); err != nil {
		t.Errorf("Parsed: %v", err)
	}
}

func TestCredencialesRechazadasEsTerminal(t *testing.T) {
	intentos := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		intentos++
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(
			`Digest qop="auth", realm=%q, nonce=%q`, testRealm, testNonce))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := clientFor(srv).GetTime(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("esperaba ErrUnauthorized, hubo %v", err)
	}
	// Un reintento y nada más: la cámara bloquea el usuario tras varios fallos.
	if intentos != 2 {
		t.Errorf("%d intentos, esperaba 2 (desafío + reintento)", intentos)
	}
}

func TestResponseStatusDeErrorConHTTP200(t *testing.T) {
	srv := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Caso real: HTTP 200 con un statusCode de error en el cuerpo.
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<ResponseStatus version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
<requestURL>/ISAPI/System/time</requestURL>
<statusCode>4</statusCode>
<statusString>Invalid Operation</statusString>
<subStatusCode>invalidOperation</subStatusCode>
</ResponseStatus>`)
	})
	defer srv.Close()

	err := clientFor(srv).PutTime(context.Background(), &Time{TimeMode: "NTP", TimeZone: "CST+5:00:00"})
	if err == nil {
		t.Fatal("un statusCode 4 con HTTP 200 debía ser error")
	}
	var status *ResponseStatus
	if !errors.As(err, &status) || status.StatusCode != 4 {
		t.Errorf("esperaba un *ResponseStatus con statusCode 4, hubo %v", err)
	}
}

func TestPutNTPServersEnvia(t *testing.T) {
	var recibido string
	srv := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		recibido = string(buf[:n])
		fmt.Fprint(w, `<ResponseStatus><statusCode>1</statusCode><statusString>OK</statusString></ResponseStatus>`)
	})
	defer srv.Close()

	in := &NTPServerList{Servers: []NTPServer{{
		ID:                   "1",
		AddressingFormatType: "hostname",
		HostName:             "ntp2.inm.gov.co",
		PortNo:               123,
		SynchronizeInterval:  60,
	}}}
	if err := clientFor(srv).PutNTPServers(context.Background(), in); err != nil {
		t.Fatalf("PutNTPServers: %v", err)
	}
	for _, esperado := range []string{
		`<NTPServerList version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">`,
		"<hostName>ntp2.inm.gov.co</hostName>",
		"<synchronizeInterval>60</synchronizeInterval>",
	} {
		if !strings.Contains(recibido, esperado) {
			t.Errorf("el cuerpo no contiene %q; fue:\n%s", esperado, recibido)
		}
	}
	// El namespace va solo en la raíz, no repetido en cada hijo.
	if strings.Count(recibido, "xmlns=") != 1 {
		t.Errorf("el xmlns debía aparecer una sola vez; fue:\n%s", recibido)
	}
}

func TestPutNTPServersRechazaListaVacia(t *testing.T) {
	c := New("127.0.0.1:1", testUser, testPass, time.Second)
	if err := c.PutNTPServers(context.Background(), &NTPServerList{}); err == nil {
		t.Error("una lista vacía borraría la configuración: debía rechazarse")
	}
}

func TestTestNTPServer(t *testing.T) {
	srv := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<NTPTestResult version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
<errorCode>0</errorCode>
<errorDescription>ok</errorDescription>
</NTPTestResult>`)
	})
	defer srv.Close()

	got, err := clientFor(srv).TestNTPServer(context.Background(), &NTPTestDescription{
		AddressingFormatType: "hostname", HostName: "ntp2.inm.gov.co", PortNo: 123,
	})
	if err != nil {
		t.Fatalf("TestNTPServer: %v", err)
	}
	if !got.Reachable() {
		t.Errorf("errorCode 0 con \"ok\" debía ser alcanzable: %+v", got)
	}
}

func TestTolerantTime(t *testing.T) {
	casos := []struct {
		valor string
		ok    bool
	}{
		{"2026-08-12T13:07:06-05:00", true}, // lo que devuelve GET /System/time
		{"2026-08-12T11:50:47-5:00", true},  // lo que envía el push de eventos
		{"2026-08-12T11:50:47", true},       // sin offset
		{"2026-08-12T11:50:47+5:30", true},
		{"no es una fecha", false},
		{"", false},
	}
	for _, c := range casos {
		_, err := tolerantTime(c.valor)
		if (err == nil) != c.ok {
			t.Errorf("tolerantTime(%q): err=%v, esperaba ok=%v", c.valor, err, c.ok)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	// El desafío exacto que devolvió la cámara verificada.
	ch, err := parseChallenge(`Digest qop="auth", realm="IP Camera(K9754)", nonce="4e5755795a6d4d344e3255365a6a55324e5455354e44413d", stale="FALSE"`)
	if err != nil {
		t.Fatalf("parseChallenge: %v", err)
	}
	if ch.realm != testRealm || ch.nonce != testNonce || ch.qop != "auth" {
		t.Errorf("desafío mal parseado: %+v", ch)
	}
	if _, err := parseChallenge(`Basic realm="x"`); err == nil {
		t.Error("un desafío Basic debía dar error")
	}
	if _, err := parseChallenge(`Digest realm="x", algorithm=SHA-256`); err == nil {
		t.Error("un algoritmo no soportado debía dar error")
	}
}

// El namespace del dispositivo se conserva en el ida y vuelta, que es lo que hace que
// funcione con las tres variantes vistas en campo.
func TestConservaNamespaceDelDispositivo(t *testing.T) {
	in := &Time{TimeMode: "NTP", TimeZone: "CST+5:00:00", Xmlns: "urn:psialliance-org"}
	body := *in
	fillEnvelope(&body.Version, &body.Xmlns)
	data, err := xml.Marshal(&body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `xmlns="urn:psialliance-org"`) {
		t.Errorf("debía conservar el namespace del dispositivo: %s", data)
	}
}
