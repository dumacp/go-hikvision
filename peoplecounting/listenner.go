package peoplecounting

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
)

//Listen function to listen events
func Listen(quit chan int, socket string, wError *log.Logger) <-chan interface{} {
	wError.Println("listennnnn")

	ch := make(chan interface{})
	h1 := func(w http.ResponseWriter, req *http.Request) {
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
			ctype := req.Header.Get("Content-Type")
			if ok := strings.Contains(ctype, "application/xml"); !ok {
				reader, err := req.GetBody()
				if err != nil {
					return
				}
				event, err := parseXMLEvent(reader)
				if err != nil {
					return
				}
				select {
				case ch <- event:
				//Timeout
				case <-time.After(3 * time.Second):
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\": \"OK\"}"))
	}

	m := http.NewServeMux()
	m.HandleFunc("/", h1)

	srv := &http.Server{
		Addr:           ":8080",
		Handler:        m,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		// defer close(ch)
		err := srv.ListenAndServe()
		wError.Println(err)
	}()

	ctx, cancel := context.WithCancel(context.Background())

	go func(cancel context.CancelFunc) {
		select {
		case <-quit:
			wError.Println("shutdown server http")
			cancel()
		}
	}(cancel)
	go func(ctx context.Context) {
		select {
		case <-ctx.Done():
			srv.Shutdown(ctx)
			wError.Println("shutdown server http")
		}
	}(ctx)

	return ch
}