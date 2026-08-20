package main

import (
	"fmt"
	"strconv"
	"strings"
)

// doorBoolFlags collects the occurrences of -zeroOpenState y -countWithCloseDoor verbatim,
// para poder distinguir después las dos formas aceptadas:
//
//	-zeroOpenState=true      posicional: la n-ésima aparición es la puerta n-1
//	-zeroOpenState 1=true    explícita: el id va escrito
//
// El valor se guarda crudo en vez de convertirlo acá, porque la conversión depende de la
// forma: la posicional tiene que conservar la manga ancha histórica —cualquier cosa distinta
// de TRUE es false— y la explícita no.
type doorBoolFlags []string

func (i *doorBoolFlags) String() string {
	return fmt.Sprintf("%v", *i)
}

func (i *doorBoolFlags) Set(value string) error {
	*i = append(*i, strings.TrimSpace(value))
	return nil
}

// resolveDoorBools resuelve las ocurrencias a un mapa por puerta.
//
// Mismo criterio que resolveCameras: mezclar las dos formas es error, no un intento de
// adivinar. `name` es el nombre del flag, solo para que el mensaje diga cuál falló.
//
// La diferencia con resolveCameras está en la manga ancha de la forma posicional: ahí
// cualquier valor distinto de TRUE es false, sin avisar, porque así se comporta desde 1.0.25
// y hay equipos en campo dependiendo de eso. En la forma explícita, en cambio, un valor que
// no sea true o false es error: es sintaxis nueva y no hay nada que conservar, y un
// `1=tru` que quedara en false en silencio es justo el error que se está tratando de evitar.
func resolveDoorBools(name string, raw []string) (map[int]bool, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var explicitas int
	for _, v := range raw {
		if strings.Contains(v, "=") {
			explicitas++
		}
	}
	out := make(map[int]bool, len(raw))

	switch {
	case explicitas == 0:
		for id, v := range raw {
			if id > maxDoorID {
				return nil, fmt.Errorf("-%s tiene %d apariciones, más que las %d puertas "+
					"admitidas", name, len(raw), maxDoorID+1)
			}
			out[id] = strings.EqualFold(v, "TRUE")
		}
		return out, nil
	case explicitas != len(raw):
		return nil, fmt.Errorf("-%s mezcla las dos formas: %v. Usá todas con id explícito "+
			"(id=valor) o todas posicionales, no una mezcla", name, raw)
	}

	for _, v := range raw {
		corte := strings.Index(v, "=")
		texto, valor := v[:corte], v[corte+1:]
		id, err := strconv.Atoi(texto)
		if err != nil {
			return nil, fmt.Errorf("-%s %q: %q no es un id de puerta", name, v, texto)
		}
		if id < 0 || id > maxDoorID {
			return nil, fmt.Errorf("-%s %q: el id de puerta %d está fuera de 0..%d",
				name, v, id, maxDoorID)
		}
		if _, ok := out[id]; ok {
			return nil, fmt.Errorf("-%s repite el id de puerta %d", name, id)
		}
		switch {
		case strings.EqualFold(valor, "true"):
			out[id] = true
		case strings.EqualFold(valor, "false"):
			out[id] = false
		default:
			return nil, fmt.Errorf("-%s %q: %q no es true ni false", name, v, valor)
		}
	}
	return out, nil
}

// describeDoorBools arma la línea del banner. Imprime el id de cada puerta, porque el slice
// crudo `[false true]` esconde justamente el índice, que es lo único que importa acá.
func describeDoorBools(m map[int]bool) string {
	if len(m) == 0 {
		return "sin configurar (default)"
	}
	partes := make([]string, 0, len(m))
	// Se recorre por id y no por el mapa, para que la línea salga siempre en el mismo orden.
	for id := 0; id <= maxDoorID; id++ {
		if v, ok := m[id]; ok {
			partes = append(partes, fmt.Sprintf("puerta %d = %v", id, v))
		}
	}
	return strings.Join(partes, ", ")
}

// maxDoorID bounds the explicit door id of -camera. It is a guard against a typo, not a
// modelled limit: writing `-camera 192.168.188.21:8080` —a port, not a door— would
// otherwise build a slice of 8081 entries and silently count nothing.
//
// Only doors 0 and 1 appear in the MQTT `registerMap`; a third one would be counted
// internally but not published.
const maxDoorID = 15

// cameraFlags collects the -camera occurrences verbatim. Two forms are accepted:
//
//	-camera 192.168.188.21      positional: the n-th occurrence is door n-1
//	-camera 192.168.188.21:1    explicit: the id is written down
//
// The explicit form exists because the positional one is easy to get wrong in the most
// common deployment: a vehicle with only the back camera. Writing a single
// `-camera 192.168.188.21` makes it door **0**, while the historical rule classified that
// same address as door 1 — so the counters would silently move from `inputs1` to `inputs0`
// and the platform would see one series freeze and another start from zero.
type cameraFlags []string

func (i *cameraFlags) String() string {
	return fmt.Sprintf("%v", *i)
}

func (i *cameraFlags) Set(value string) error {
	*i = append(*i, strings.TrimSpace(value))
	return nil
}

// resolveCameras turns the -camera occurrences into a slice indexed by door id.
//
// Mixing both forms is an error instead of a best effort. A positional occurrence next to
// an explicit one has no obvious meaning —does it take the next free slot, or the next
// position?— and any answer would be a rule to remember. Failing at startup is cheaper
// than a fleet that counts the wrong door.
func resolveCameras(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var explicitas int
	for _, v := range raw {
		if strings.Contains(v, ":") {
			explicitas++
		}
	}
	switch {
	case explicitas == 0:
		// Forma posicional, idéntica a la histórica.
		return raw, nil
	case explicitas != len(raw):
		return nil, fmt.Errorf("-camera mezcla las dos formas: %v. Usá todas con id "+
			"explícito (ip:id) o todas posicionales, no una mezcla", raw)
	}

	porID := make(map[int]string, len(raw))
	mayor := -1
	for _, v := range raw {
		corte := strings.LastIndex(v, ":")
		ip, texto := v[:corte], v[corte+1:]
		if len(ip) == 0 {
			return nil, fmt.Errorf("-camera %q no tiene dirección antes del id", v)
		}
		id, err := strconv.Atoi(texto)
		if err != nil {
			return nil, fmt.Errorf("-camera %q: %q no es un id de puerta", v, texto)
		}
		if id < 0 || id > maxDoorID {
			return nil, fmt.Errorf("-camera %q: el id de puerta %d está fuera de 0..%d. "+
				"Si querías un puerto TCP, no se admite: la extracción de video usa RTSP en 554",
				v, id, maxDoorID)
		}
		if otra, ok := porID[id]; ok {
			return nil, fmt.Errorf("-camera repite el id de puerta %d: %q y %q", id, otra, ip)
		}
		porID[id] = ip
		if id > mayor {
			mayor = id
		}
	}
	// Los ids que nadie reclamó quedan vacíos, y todo el binario ya los salta: doorID no
	// los compara, CameraActor no los revisa y VideoActor no extrae de ellos.
	out := make([]string, mayor+1)
	for id, ip := range porID {
		out[id] = ip
	}
	return out, nil
}

// describeCameras arma la línea del banner de arranque. Imprime el id de cada puerta en
// vez del slice crudo, porque `[ 192.168.188.21]` esconde justamente el índice, que es lo
// único que importa acá.
func describeCameras(cams []string) string {
	partes := make([]string, 0, len(cams))
	for id, ip := range cams {
		if len(ip) == 0 {
			continue
		}
		partes = append(partes, fmt.Sprintf("puerta %d -> %s", id, ip))
	}
	if len(partes) == 0 {
		return "ninguna"
	}
	return strings.Join(partes, ", ")
}
