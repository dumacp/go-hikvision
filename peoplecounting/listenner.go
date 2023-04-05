package peoplecounting

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

type Event struct {
	ID   string
	Data interface{}
}

// Listen function to listen events
func Listen(ctx context.Context, socket string, wError, wCamera *log.Logger) <-chan *Event {
	// wError.Println("listennnnn")

	ch := make(chan *Event)
	h1 := func(w http.ResponseWriter, req *http.Request) {
		defer req.Body.Close()
		switch req.Method {
		case http.MethodPost:
			// disp := req.Header.Get("Content-Disposition")
			// _, params, err := mime.ParseMediaType(disp)
			// if err != nil {
			// 	return
			// }
			// name, ok := params["name"]
			// if !ok {
			// 	return
			// }
			// if ok := strings.Contains(name, "Event_Type"); !ok {
			// 	return
			// }
			// log.Printf("headers: %v", req.Header)
			ctype := req.Header.Get("Content-Type")
			if ok := strings.Contains(ctype, "application/xml"); ok {
				// if err := req.ParseForm(); err != nil {
				// 	log.Println(err)
				// }
				// log.Printf("Post from website! r.PostFrom = %v\n", req.PostForm)
				reader := req.Body

				body, err := ioutil.ReadAll(reader)
				if err != nil {
					log.Printf("Error reading body: %v", err)
					return
				}
				reader.Close()

				wCamera.Printf("%d: listen event: %s\n", time.Now().UnixNano()/1000_000, body)

				newreader := bytes.NewReader(body)

				// if err != nil {
				// 	return
				// }
				event, err := parseXMLEvent(newreader)
				// req.Body.Close()
				// reader.Close()
				if err != nil {
					log.Println(err)
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
