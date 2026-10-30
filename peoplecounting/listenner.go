package peoplecounting

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

type Event struct {
	ID   string
	Data interface{}
}

// Listen function to listen events
func Listen(ctx context.Context, socket string, wError, wWarn, wCamera *log.Logger) <-chan *Event {
	// wError.Println("listennnnn")

	ch := make(chan *Event)

	// entregar es el camino comun de las DOS formas en que la camara manda el evento:
	// el cuerpo XML pelado y la parte XML de un multipart. Esta extraido a proposito —
	// si cada rama parseara y encolara por su cuenta, las dos podrian separarse con el
	// tiempo sin que nadie lo note, y una de ellas es la de las camaras en campo.
	entregar := func(req *http.Request, cuerpo []byte) {
		wCamera.Printf("%d: listen event: %s\n", time.Now().UnixNano()/1000_000, cuerpo)

		event, err := parseXMLEvent(bytes.NewReader(cuerpo))
		if err != nil {
			// La direccion de origen es la mitad util del aviso: dice QUE camara
			// manda algo que no sabemos leer, que es lo que se necesita para ir a
			// mirarla. Sin eso el mensaje obliga a capturar el trafico en crudo.
			wWarn.Printf("evento de %s descartado: %s", req.RemoteAddr, err)
			return
		}

		id := func() string {
			if req != nil {
				parts := strings.Split(req.RemoteAddr, ":")
				if len(parts) > 0 {
					return parts[0]
				}
			}
			return ""
		}()
		fmt.Printf("remote: %v, id: %s\n", req, id)

		evt := &Event{
			ID:   id,
			Data: event,
		}
		select {
		case ch <- evt:
		//Timeout
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
		}
	}

	h1 := func(w http.ResponseWriter, req *http.Request) {
		defer req.Body.Close()
		switch req.Method {
		case http.MethodPost:
			ctype := req.Header.Get("Content-Type")
			switch {
			// El multipart va PRIMERO porque su media type no contiene "xml" y la
			// rama de abajo no lo atraparia igual; el orden solo documenta la
			// intencion.
			//
			// La DS-2XM6825G1 con firmware V4.2.2 adjunta la foto del cruce y manda
			// "multipart/form-data; charset=\"UTF-8\"; boundary=...". Antes el cuerpo
			// entero se descartaba con un WARN y la camara recibia 200: esa puerta
			// dejaba de contar sin que nada fallara a la vista. Se descubrio al
			// actualizar el firmware, no por diseno.
			case strings.Contains(ctype, "multipart"):
				parte, err := xmlDeMultipart(req.Body, ctype)
				if err != nil {
					wWarn.Printf("multipart de %s descartado: %s", req.RemoteAddr, err)
					break
				}
				entregar(req, parte)

			// The camera posts "application/xml; charset=UTF-8", but ISAPI documents
			// "text/xml" as well for this listening mode, so match any XML media type
			// instead of a single spelling.
			//
			// Este es el camino de las camaras en campo (DS-2XM6825G0 y la G1 antes de
			// V4.2.2) y NO cambia: lee el cuerpo entero y lo entrega tal cual.
			case strings.Contains(ctype, "xml"):
				body, err := ioutil.ReadAll(req.Body)
				if err != nil {
					log.Printf("Error reading body: %v", err)
					return
				}
				req.Body.Close()
				entregar(req, body)

			default:
				// The 200 OK below is answered even when the body is dropped, so
				// without this log a wrong Content-Type looks healthy from both ends.
				wWarn.Printf("discarded POST body from %s, unsupported Content-Type %q",
					req.RemoteAddr, ctype)
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\": \"OK\"}"))
	}

	m := http.NewServeMux()
	m.HandleFunc("/", h1)
	// m.HandleFunc("/http://192.168.188.23:8088/", h1)

	srv := &http.Server{
		Addr:           socket,
		Handler:        m,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		// defer close(ch)
		err := srv.ListenAndServe()
		close(ch)
		wError.Println(err)
	}()

	go func(ctx context.Context) {
		<-ctx.Done()
		srv.Shutdown(ctx)
		wError.Println("shutdown server http")
	}(ctx)

	return ch
}

// maxParteXML acota lo que se lee de una parte del multipart.
//
// Las otras partes son imagenes y pueden pesar cientos de kB; el XML de un evento no
// llega a 2 kB. Sin un tope, una parte mal rotulada haria que nos tragaramos la foto
// entera en memoria para despues descartarla al no parsear.
const maxParteXML = 256 << 10

// xmlDeMultipart devuelve la parte XML de un cuerpo multipart.
//
// Solo devuelve el XML: la foto adjunta se descarta sin leerla. Hoy nada la consume, y
// leerla seria cargar cientos de kB por evento en un equipo embebido para tirarlos.
// Cuando haga falta, este es el unico lugar que hay que tocar.
//
// La parte se elige por su Content-Type cuando lo trae, y si no por la forma del
// contenido. La camara NO rotula sus partes de forma fiable —el header observado trae
// charset y boundary pero las partes pueden venir sin tipo—, asi que una seleccion por
// nombre fijo se romperia con el proximo firmware.
func xmlDeMultipart(cuerpo io.Reader, ctype string) ([]byte, error) {
	_, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		return nil, fmt.Errorf("Content-Type %q ilegible: %w", ctype, err)
	}
	frontera, ok := params["boundary"]
	if !ok || frontera == "" {
		return nil, fmt.Errorf("multipart sin boundary en %q", ctype)
	}

	lector := multipart.NewReader(cuerpo, frontera)
	vistas := 0
	for {
		parte, err := lector.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("leyendo parte %d: %w", vistas+1, err)
		}
		vistas++

		tipo := parte.Header.Get("Content-Type")
		if tipo != "" && !strings.Contains(tipo, "xml") {
			// Una imagen u otra cosa rotulada: se salta sin leer el contenido.
			parte.Close()
			continue
		}

		datos, err := ioutil.ReadAll(io.LimitReader(parte, maxParteXML))
		parte.Close()
		if err != nil {
			return nil, fmt.Errorf("leyendo parte %d: %w", vistas, err)
		}
		// Sin Content-Type hay que mirar el contenido. Se exige que empiece por '<'
		// para no entregarle al parser el principio de un JPEG.
		if tipo == "" && !bytes.HasPrefix(bytes.TrimSpace(datos), []byte("<")) {
			continue
		}
		return datos, nil
	}
	return nil, fmt.Errorf("ninguna de las %d partes trae XML", vistas)
}
