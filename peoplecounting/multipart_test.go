package peoplecounting

import (
	"bytes"
	"context"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// cuerpoMultipart arma un POST como el que manda la DS-2XM6825G1 con firmware V4.2.2:
// una parte XML y una parte de imagen. El boundary y la forma del Content-Type son los
// observados en campo, con charset entre comillas.
func cuerpoMultipart(xml string, conImagen bool) (cuerpo []byte, ctype string) {
	const b = "7daf10c20d06"
	var buf bytes.Buffer
	buf.WriteString("--" + b + "\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"Event_Data\"\r\n")
	buf.WriteString("Content-Type: application/xml\r\n\r\n")
	buf.WriteString(xml)
	buf.WriteString("\r\n")
	if conImagen {
		buf.WriteString("--" + b + "\r\n")
		buf.WriteString("Content-Disposition: form-data; name=\"Picture\"; filename=\"p.jpg\"\r\n")
		buf.WriteString("Content-Type: image/jpeg\r\n\r\n")
		buf.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'})
		buf.WriteString("\r\n")
	}
	buf.WriteString("--" + b + "--\r\n")
	return buf.Bytes(), `multipart/form-data; charset="UTF-8"; boundary=` + b
}

// TestXMLDeMultipart: la parte XML se encuentra y la imagen no estorba.
func TestXMLDeMultipart(t *testing.T) {
	cuerpo, ctype := cuerpoMultipart(xmlG1, true)
	datos, err := xmlDeMultipart(bytes.NewReader(cuerpo), ctype)
	if err != nil {
		t.Fatalf("no deberia fallar: %s", err)
	}
	if !bytes.Contains(datos, []byte("<eventType>counting</eventType>")) {
		t.Errorf("la parte devuelta no es el XML del evento: %.80s", datos)
	}
	// Y lo devuelto tiene que poder parsearse tal cual, sin limpieza extra.
	ev, err := parseXMLEvent(bytes.NewReader(datos))
	if err != nil {
		t.Fatalf("el XML extraido no parsea: %s", err)
	}
	c, ok := ev.(*EventNotificationAlertCounting)
	if !ok {
		t.Fatalf("tipo %T, quiero *EventNotificationAlertCounting", ev)
	}
	if c.Counting.Enter != 2 || c.Counting.Exit != 3 {
		t.Errorf("enter=%d exit=%d, quiero 2 y 3", c.Counting.Enter, c.Counting.Exit)
	}
}

// TestXMLDeMultipartSinRotular: la camara puede mandar la parte sin Content-Type. Se
// elige por la forma del contenido, no por un nombre fijo que el proximo firmware
// cambiaria.
func TestXMLDeMultipartSinRotular(t *testing.T) {
	const b = "abc123"
	var buf bytes.Buffer
	buf.WriteString("--" + b + "\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"x\"\r\n\r\n")
	buf.WriteString(xmlG1)
	buf.WriteString("\r\n--" + b + "--\r\n")
	datos, err := xmlDeMultipart(bytes.NewReader(buf.Bytes()), "multipart/form-data; boundary="+b)
	if err != nil {
		t.Fatalf("no deberia fallar: %s", err)
	}
	if !bytes.Contains(datos, []byte("<eventType>counting</eventType>")) {
		t.Errorf("no se eligio la parte correcta")
	}
}

// TestXMLDeMultipartSinXML: un cuerpo que solo trae imagen debe dar error con su causa,
// no devolver algo vacio que el parser rechace despues con un mensaje peor.
func TestXMLDeMultipartSinXML(t *testing.T) {
	const b = "soloimagen"
	var buf bytes.Buffer
	buf.WriteString("--" + b + "\r\n")
	buf.WriteString("Content-Type: image/jpeg\r\n\r\n")
	buf.Write([]byte{0xFF, 0xD8, 0xFF})
	buf.WriteString("\r\n--" + b + "--\r\n")
	if _, err := xmlDeMultipart(bytes.NewReader(buf.Bytes()), "multipart/form-data; boundary="+b); err == nil {
		t.Fatal("un multipart sin XML debe dar error")
	}
}

// TestXMLDeMultipartSinBoundary: sin boundary no hay nada que recorrer, y el error debe
// decirlo en vez de fallar mas adelante.
func TestXMLDeMultipartSinBoundary(t *testing.T) {
	_, err := xmlDeMultipart(strings.NewReader("lo que sea"), "multipart/form-data")
	if err == nil {
		t.Fatal("sin boundary debe dar error")
	}
	if !strings.Contains(err.Error(), "boundary") {
		t.Errorf("el error debe nombrar el boundary: %s", err)
	}
}

// TestListenAceptaLasDosFormas es la prueba que importa para el despliegue: el MISMO
// servidor atiende a las camaras en campo (XML pelado) y a la G1 con V4.2.2
// (multipart). Si alguien rompe una de las dos ramas, esto falla.
func TestListenAceptaLasDosFormas(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mudo := log.New(ioutil.Discard, "", 0)
	puerto := puertoLibre(t)
	ch := Listen(ctx, puerto, mudo, mudo, mudo)

	esperarPuerto(t, puerto)

	casos := []struct {
		nombre string
		ctype  string
		cuerpo []byte
		quiero string // fragmento que debe traer el evento parseado
	}{
		{"camara en campo: XML pelado", "application/xml; charset=UTF-8", []byte(xmlG0), "PeopleCounting"},
		{"G1 con V4.2.2: multipart", "", nil, "counting"},
	}
	casos[1].cuerpo, casos[1].ctype = cuerpoMultipart(xmlG1, true)

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			// El canal se lee EN PARALELO con el POST a proposito: el handler encola
			// el evento antes de responder, y su envio al canal tiene un tope de 3 s.
			// Si el test esperara la respuesta HTTP primero, los dos quedarian
			// bloqueados, el evento se perderia por timeout y el fallo apuntaria al
			// parser en vez de al arnes.
			recibido := make(chan *Event, 1)
			go func() {
				select {
				case e := <-ch:
					recibido <- e
				case <-time.After(5 * time.Second):
					recibido <- nil
				}
			}()

			req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1"+puerto+"/", bytes.NewReader(c.cuerpo))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", c.ctype)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST: %s", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			select {
			case evt := <-recibido:
				if evt == nil || evt.Data == nil {
					t.Fatal("llego un evento vacio")
				}
				switch d := evt.Data.(type) {
				case *EventNotificationAlertPeopleConting:
					if d.EventType != c.quiero {
						t.Errorf("eventType %q, quiero %q", d.EventType, c.quiero)
					}
				case *EventNotificationAlertCounting:
					if d.EventType != c.quiero {
						t.Errorf("eventType %q, quiero %q", d.EventType, c.quiero)
					}
				default:
					t.Errorf("tipo inesperado %T", d)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("el evento nunca llego al canal")
			}
		})
	}
}

func puertoLibre(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()[strings.LastIndex(l.Addr().String(), ":"):]
}

func esperarPuerto(t *testing.T, puerto string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		c, err := net.DialTimeout("tcp", "127.0.0.1"+puerto, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("el servidor no levanto")
}
