#!/bin/bash
# Descubrimiento de solo lectura contra una cámara Hikvision por ISAPI.
#
#   CAM_IP=192.168.186.91 CAM_USER=admin CAM_PASS='...' ./check-camera.sh [canal]
#
# Solo hace GET: no cambia nada en la cámara. El canal por defecto es 1.
#
# CUIDADO con las credenciales: la spec (ISAPI_general.pdf §3.1) devuelve los
# intentos restantes y BLOQUEA el usuario al agotarlos. El script aborta al
# primer 401 en vez de reintentar. Nunca pruebes contraseñas a ciegas.

set -uo pipefail

IP="${CAM_IP:?falta CAM_IP}"
USER="${CAM_USER:?falta CAM_USER}"
PASS="${CAM_PASS:?falta CAM_PASS}"
CH="${1:-1}"

get() {
    local path="$1" desc="$2"
    printf '\n\033[1m== %s\033[0m\n   GET %s\n' "$desc" "$path"
    local body code
    body="$(curl -sS --digest -u "$USER:$PASS" -m 10 -w '\n__CODE__%{http_code}' \
            "http://$IP$path" 2>&1)"
    code="${body##*__CODE__}"
    body="${body%__CODE__*}"
    printf '   HTTP %s\n' "$code"
    if [[ "$code" == "401" ]]; then
        echo "   ABORTA: credenciales rechazadas. No reintento para no bloquear el usuario." >&2
        exit 2
    fi
    # statusCode de error puede venir con HTTP 200: mostrar siempre el cuerpo
    sed 's/^/   | /' <<<"$body"
}

echo "cámara: $IP   canal: $CH   usuario: $USER"

get "/ISAPI/System/deviceInfo" \
    "1. Modelo y firmware (para anotar en la skill)"

get "/ISAPI/System/Video/capabilities" \
    "2. ¿Soporta conteo? (isSupportCounting)"

get "/ISAPI/System/Video/inputs/channels/$CH/counting" \
    "3. Configuración de conteo actual (enabled, Demarcation, ChildFilter)"

get "/ISAPI/System/Video/inputs/channels/$CH/counting/status" \
    "4. Estado del conteo y doorStatus  <-- la oportunidad de respaldo de puerta"

get "/ISAPI/System/Video/inputs/channels/$CH/counting/capabilities" \
    "5. Capacidades de conteo (¿reporta doorStatus? ¿ChildFilter?)"

get "/ISAPI/Event/notification/httpHosts" \
    "6. httpHosts configurados  <-- ¿a dónde empuja hoy? ¿parameterFormatType?"

get "/ISAPI/Event/notification/httpHosts/capabilities" \
    "7. Capacidad de httpHosts (hostNumber)"

get "/ISAPI/Event/triggers" \
    "8. Triggers y notificationRecurrence  <-- DECISIVO para el doble conteo de tamper"

printf '\n\033[1mListo.\033[0m Nada fue modificado en la cámara.\n'
