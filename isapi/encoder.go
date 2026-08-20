package isapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// ChannelEncoder es la configuración de codificación de un canal de streaming.
//
// Esta es la parte que **exige reiniciar la cámara**: el PUT responde statusCode 7 y el
// cambio queda inerte hasta el reinicio, a diferencia del horario de grabación y del NTP,
// que se aplican en caliente.
type ChannelEncoder struct {
	XMLName xml.Name `xml:"StreamingChannel"`
	Video   struct {
		CodecType string `xml:"videoCodecType"`
		Width     int    `xml:"videoResolutionWidth"`
		Height    int    `xml:"videoResolutionHeight"`
		// MaxFrameRate viene en centi-fps: 2000 son 20 fps. Es una de las trampas del
		// formato, y confundirla lleva a creer que la cámara graba a 2000 fps.
		MaxFrameRate int `xml:"maxFrameRate"`
		// KeyFrameInterval está en milisegundos; GovLength en frames.
		KeyFrameInterval int    `xml:"keyFrameInterval"`
		GovLength        int    `xml:"GovLength"`
		QualityControl   string `xml:"videoQualityControlType"`
		VBRUpperCap      int    `xml:"vbrUpperCap"`
		SmartCodec       struct {
			Enabled bool `xml:"enabled"`
		} `xml:"SmartCodec"`
	} `xml:"Video"`
}

// FrameRate devuelve los fps reales a partir de los centi-fps declarados.
func (c *ChannelEncoder) FrameRate() float64 { return float64(c.Video.MaxFrameRate) / 100 }

// GetChannelEncoder lee la configuración de codificación de un canal.
func (c *Client) GetChannelEncoder(ctx context.Context, channel int) (*ChannelEncoder, error) {
	out := new(ChannelEncoder)
	if err := c.get(ctx, fmt.Sprintf("/ISAPI/Streaming/channels/%d", channel), out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetChannelRaw devuelve el XML crudo del canal.
//
// Se trabaja sobre el crudo por lo mismo que el horario de grabación: el documento trae
// transporte, multicast, audio y overlays, y reconstruirlo desde un struct con los campos
// que interesan borraría todo lo demás.
func (c *Client) GetChannelRaw(ctx context.Context, channel int) ([]byte, error) {
	return c.do(ctx, "GET", fmt.Sprintf("/ISAPI/Streaming/channels/%d", channel), nil)
}

// PutChannelRaw escribe el XML del canal.
//
// Devuelve el error tal cual: quien llama debe reconocer statusCode 7 como "aplicado,
// falta reiniciar" y no como fallo.
func (c *Client) PutChannelRaw(ctx context.Context, channel int, body []byte) error {
	_, err := c.do(ctx, "PUT", fmt.Sprintf("/ISAPI/Streaming/channels/%d", channel), body)
	return err
}

// Reboot reinicia la cámara.
//
// Tiene costo real y quien llama debe decidirlo con cuidado: el reinicio **corta el tramo
// grabado** —medido: unos 70 segundos sin cobertura, y pedir un instante del hueco
// devuelve 500— y durante el arranque la cámara no envía eventos, así que los pasajeros
// que cruzan en esa ventana no se cuentan.
func (c *Client) Reboot(ctx context.Context) error {
	_, err := c.do(ctx, "PUT", "/ISAPI/System/reboot", nil)
	return err
}

// SetVideoField reemplaza el valor de un elemento dentro del bloque <Video> del XML crudo.
//
// Devuelve el XML nuevo y si cambió algo. Si el elemento no existe no es error: no todos
// los modelos exponen los mismos campos, y forzar su presencia haría fallar el ciclo en
// una cámara distinta.
func SetVideoField(raw []byte, field, value string) ([]byte, bool, error) {
	ini, fin, err := blockRange(raw, "Video")
	if err != nil {
		return raw, false, err
	}
	nuevo, cambio := replaceElement(raw[ini:fin], field, value)
	if !cambio {
		return raw, false, nil
	}
	out := make([]byte, 0, len(raw)+16)
	out = append(out, raw[:ini]...)
	out = append(out, nuevo...)
	out = append(out, raw[fin:]...)
	return out, true, nil
}

// SetSmartCodec habilita o desactiva H.264+ en el XML crudo.
//
// Es el ajuste que más importa para el video: con SmartCodec activo y escena quieta la
// cámara graba un keyframe cada varios segundos, y el clip parece congelado. Medido: 49
// frames con un hueco de 5.7 s contra 201 continuos al desactivarlo.
func SetSmartCodec(raw []byte, enabled bool) ([]byte, bool, error) {
	ini, fin, err := blockRange(raw, "SmartCodec")
	if err != nil {
		// El modelo no lo expone: no hay nada que hacer y no es un fallo.
		return raw, false, nil
	}
	nuevo, cambio := replaceElement(raw[ini:fin], "enabled", strconv.FormatBool(enabled))
	if !cambio {
		return raw, false, nil
	}
	out := make([]byte, 0, len(raw)+8)
	out = append(out, raw[:ini]...)
	out = append(out, nuevo...)
	out = append(out, raw[fin:]...)
	return out, true, nil
}

// blockRange devuelve el rango del bloque <name>...</name>.
func blockRange(raw []byte, name string) (int, int, error) {
	s := string(raw)
	abre := strings.Index(s, "<"+name+">")
	if abre < 0 {
		return 0, 0, fmt.Errorf("el documento no tiene un bloque <%s>", name)
	}
	cierra := strings.Index(s[abre:], "</"+name+">")
	if cierra < 0 {
		return 0, 0, fmt.Errorf("el bloque <%s> no está cerrado", name)
	}
	return abre, abre + cierra + len("</"+name+">"), nil
}

// replaceElement sustituye el texto del primer <field>...</field> del fragmento. Devuelve
// false si no lo encuentra o si el valor ya era el pedido, que es lo que hace idempotente
// al ciclo.
func replaceElement(frag []byte, field, value string) ([]byte, bool) {
	s := string(frag)
	abre := strings.Index(s, "<"+field+">")
	if abre < 0 {
		return frag, false
	}
	desde := abre + len("<"+field+">")
	cierra := strings.Index(s[desde:], "</"+field+">")
	if cierra < 0 {
		return frag, false
	}
	if s[desde:desde+cierra] == value {
		return frag, false
	}
	return []byte(s[:desde] + value + s[desde+cierra:]), true
}
