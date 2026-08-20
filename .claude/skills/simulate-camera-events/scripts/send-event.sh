#!/bin/bash
# Inyecta un evento XML de cámara Hikvision al listener de go-hikvision.
#
#   send-event.sh <tipo> [enter] [exit] [host:puerto]
#
#   tipo  : counting | timerange | signaltrigger | scenechange | shelteralarm
#   enter : acumulado de entradas (default 1) -- solo counting/timerange
#   exit  : acumulado de salidas  (default 1) -- solo counting/timerange
#   host  : default 127.0.0.1:8088 (el -socket del binario)
#
# Recuerda: desde localhost el evento se atribuye a la puerta id 0, y el binario
# necesita -countWithCloseDoor=true para no descartar el conteo. Ver SKILL.md.

set -euo pipefail

REF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../references" && pwd)"

tipo="${1:-counting}"
enter="${2:-1}"
exit_="${3:-1}"
target="${4:-127.0.0.1:8088}"

case "$tipo" in
    counting)      fixture="people-counting-realtime.xml" ;;
    timerange)     fixture="people-counting-timerange.xml" ;;
    signaltrigger) fixture="people-counting-signaltrigger.xml" ;;
    scenechange)   fixture="scenechangedetection.xml" ;;
    shelteralarm)  fixture="shelteralarm.xml" ;;
    *)
        echo "tipo desconocido: $tipo" >&2
        echo "usa: counting | timerange | signaltrigger | scenechange | shelteralarm" >&2
        exit 1
        ;;
esac

src="$REF_DIR/$fixture"
if [[ ! -f "$src" ]]; then
    echo "no existe el fixture: $src" >&2
    exit 1
fi

# Eventos con fecha anterior al último visto se descartan, por eso siempre va "ahora".
#
# La cámara real (DS-2XM6825G0, V5.5.850) emite el offset SIN cero inicial:
# "2026-08-12T11:50:47-5:00", que no es RFC3339 válido. Es justo lo que normaliza
# parseDateTime con su regex, así que reproducirlo es parte de la prueba: con el
# offset bien formado ese camino nunca se ejercita.
# Poné RFC3339_ESTRICTO=1 para enviar el offset canónico (-05:00) en su lugar.
now="$(date +%Y-%m-%dT%H:%M:%S%:z)"
if [[ -z "${RFC3339_ESTRICTO:-}" ]]; then
    now="$(sed -E 's/([+-])0([0-9]:[0-9]{2})$/\1\2/' <<<"$now")"
fi

body="$(sed -e "s|__DATETIME__|${now}|g" \
            -e "s|__ENTER__|${enter}|g" \
            -e "s|__EXIT__|${exit_}|g" "$src")"

echo "--> POST http://${target}/  (${tipo}, enter=${enter}, exit=${exit_}, dateTime=${now})"

# El Content-Type es obligatorio: sin "application/xml" el handler descarta el
# cuerpo en silencio y responde 200 igual.
curl -sS -w '\n<-- HTTP %{http_code}\n' \
     -X POST "http://${target}/" \
     -H 'Content-Type: application/xml; charset="UTF-8"' \
     --data-binary "$body"
