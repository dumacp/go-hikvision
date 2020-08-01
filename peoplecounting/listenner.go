package peoplecounting

import (
	"log"
	"net/http"
	"strings"
)

//Listen function to listen events
func Listen(socket string) {

	
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
				events, err := parseXMLEvent(reader)
				if err != nil {
					return
				}

			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\": \"OK\"}"))
	}

	http.HandleFunc("/", h1)

	err := http.ListenAndServe(socket, nil)
	log.Fatal(err)
}
