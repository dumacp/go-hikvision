package main

import (
	"fmt"
	"strconv"
	"strings"
)

// zeroFlags y closeFlags recogen las apariciones de -zeroOpenState y -countWithCloseDoor.
// La sintaxis no cambió nunca: un booleano por aparición, y cualquier valor distinto de
// TRUE (case-insensitive) es false.
type zeroFlags []bool

func (i *zeroFlags) String() string {
	return fmt.Sprintf("%v", *i)
}

func (i *zeroFlags) Set(value string) error {
	if len(value) > 0 && strings.ToUpper(value) == "TRUE" {
		*i = append(*i, true)
	} else {
		*i = append(*i, false)
	}
	return nil
}

type closeFlags []bool

func (i *closeFlags) String() string {
	return fmt.Sprintf("%v", *i)
}

func (i *closeFlags) Set(value string) error {
	if len(value) > 0 && strings.ToUpper(value) == "TRUE" {
		*i = append(*i, true)
	} else {
		*i = append(*i, false)
	}
	return nil
}

// applyDoorBool aplica un switch por puerta al actor.
//
// **Una sola aparición aplica a TODAS las puertas**, no solo a la 0. Es la regla que hace
// falta: estos switches describen cómo está cableado el vehículo —si el estado "abierta"
// llega como 0 o como 1— y eso no cambia entre la puerta delantera y la trasera. Con la
// regla posicional pura, un equipo con solo la cámara trasera y un único
// `-zeroOpenState=true` configuraba la puerta 0 mientras los eventos caían en la 1, que
// quedaba con el default; y no daba error, solo descartaba conteos.
//
// Dos o más apariciones siguen siendo posicionales: la n-ésima configura la puerta n-1. Esa
// es la forma de darle un valor distinto a cada puerta y no cambia.
func applyDoorBool(values []bool, set func(id int, v bool)) {
	if len(values) == 1 {
		// Se llenan todas las puertas modelables y no solo 0 y 1, para que agregar una
		// tercera no reviva el mismo error en silencio.
		for id := 0; id <= maxDoorID; id++ {
			set(id, values[0])
		}
		return
	}
	for id, v := range values {
		set(id, v)
	}
}

// describeDoorBool arma la línea del banner. Con un solo valor dice que va a todas las
// puertas, porque el `[true]` crudo no dejaba ver a cuál aplicaba — que era justamente la
// parte que se malinterpretaba.
func describeDoorBool(values []bool) string {
	if len(values) == 1 {
		return fmt.Sprintf("%v (todas las puertas)", values[0])
	}
	partes := make([]string, 0, len(values))
	for id, v := range values {
		partes = append(partes, fmt.Sprintf("puerta %d = %v", id, v))
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
