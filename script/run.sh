#!/usr/bin/with-contenv bashio
# SPDX-License-Identifier: MIT
# Home Assistant add-on entrypoint for go-homeconnect2mqtt.
#
# Reads the user's add-on options (/data/options.json) via bashio, maps the
# scalar options onto the daemon's HC2M_* environment variables, optionally
# parses the Home Connect profile ZIP into device descriptions, writes the
# devices list to /data/devices.yaml, and finally exec's the binary so it
# becomes PID 1 and receives signals directly.
set -e

bashio::log.info "Starting go-homeconnect2mqtt add-on..."

# --- MQTT (HC2M_MQTT_SERVER is a URL: tcp://host:port or ssl://host:port) ---
if bashio::config.has_value 'mqtt_server'; then
  server="$(bashio::config 'mqtt_server')"
  case "$server" in
    *://*) export HC2M_MQTT_SERVER="$server" ;;
    *)     export HC2M_MQTT_SERVER="tcp://${server}:$(bashio::config 'mqtt_port')" ;;
  esac
  export HC2M_MQTT_LOGIN="$(bashio::config 'mqtt_login')"
  export HC2M_MQTT_PASSWORD="$(bashio::config 'mqtt_password')"
elif bashio::services.available 'mqtt'; then
  bashio::log.info "mqtt_server empty; using the Home Assistant MQTT service."
  host="$(bashio::services 'mqtt' 'host')"
  port="$(bashio::services 'mqtt' 'port')"
  scheme="tcp"
  # bashio::services prints the value ("true"/"false") AND exits 0, so the
  # value must be tested explicitly — using it as an `if` condition is always
  # true and would force ssl:// against a plaintext broker.
  if [ "$(bashio::services 'mqtt' 'ssl')" = "true" ]; then
    scheme="ssl"
  fi
  export HC2M_MQTT_SERVER="${scheme}://${host}:${port}"
  export HC2M_MQTT_LOGIN="$(bashio::services 'mqtt' 'username')"
  export HC2M_MQTT_PASSWORD="$(bashio::services 'mqtt' 'password')"
else
  bashio::log.warning "mqtt_server empty and no MQTT service offered; falling back to core-mosquitto:1883."
  export HC2M_MQTT_SERVER="tcp://core-mosquitto:1883"
fi
export HC2M_MQTT_TOPIC="$(bashio::config 'mqtt_topic')"

# --- Home Assistant discovery + misc ---
export HC2M_HASS_ENABLE="$(bashio::config 'hass_enable')"
export HC2M_LANGUAGE="$(bashio::config 'language')"
export HC2M_DEBUG="$(bashio::config 'debug')"

# --- Diagnostic web UI / Ingress (bind 0.0.0.0 so the Ingress proxy reaches it) ---
export HC2M_WEB_ENABLE="$(bashio::config 'web_enable')"
export HC2M_WEB_BIND="0.0.0.0:8080"

# --- Operator drop folder: make sure /share/homeconnect exists so the profile
# ZIP (or pre-parsed <haId>.json files) has an obvious place to be copied to.
# /share is mapped read-write by the add-on manifest. ---
SHARE_DIR="/share/homeconnect"
if mkdir -p "${SHARE_DIR}" 2>/dev/null; then
  bashio::log.info "Profile drop folder ready at ${SHARE_DIR} (copy your profile ZIP or <haId>.json files here)."
else
  bashio::log.warning "Could not create ${SHARE_DIR} (is /share mapped?)."
fi

# --- Parse profile ZIPs into descriptions + a keys inventory ---
# Source: an explicit profile_zip (single file) wins; otherwise EVERY *.zip in
# the /share/homeconnect drop folder. The inventory (keys, 0600) lets each
# device be filled from just its haId — no need to paste psk64/connection_type.
PROFILES="/data/profiles"
mkdir -p "${PROFILES}"
INVENTORY="${PROFILES}/inventory.json"
rm -f "${INVENTORY}"

ZIP_SRC=""
if bashio::config.has_value 'profile_zip'; then
  ZIP_SRC="$(bashio::config 'profile_zip')"
  if [ ! -f "${ZIP_SRC}" ]; then
    bashio::log.warning "profile_zip '${ZIP_SRC}' not found (is /share mapped?)."
    ZIP_SRC=""
  fi
elif ls "${SHARE_DIR}"/*.zip >/dev/null 2>&1; then
  ZIP_SRC="${SHARE_DIR}"
fi
if [ -n "${ZIP_SRC}" ]; then
  bashio::log.info "Parsing profiles from ${ZIP_SRC} -> ${PROFILES}"
  if ! hc-util parse "${ZIP_SRC}" --out "${PROFILES}" --inventory "${INVENTORY}"; then
    bashio::log.warning "hc-util parse failed; relying on explicit device fields."
  fi
fi

# Look up a field from the inventory by haId (empty if absent).
inv_get() { # $1=haId  $2=jq field
  [ -f "${INVENTORY}" ] || return 0
  jq -r --arg h "$1" --arg f "$2" '(.[] | select(.haId==$h) | .[$f]) // empty' "${INVENTORY}" 2>/dev/null
}

# --- Build devices.yaml from the 'devices' option list ---
DEVICES="/data/devices.yaml"
if [ -n "$(bashio::config 'devices|keys')" ]; then
  echo "devices:" > "${DEVICES}"
  for i in $(bashio::config 'devices|keys'); do
    name="$(bashio::config "devices[${i}].name")"
    host="$(bashio::config "devices[${i}].host")"
    ctype="$(bashio::config "devices[${i}].connection_type")"
    psk="$(bashio::config "devices[${i}].psk64")"
    iv="$(bashio::config "devices[${i}].iv64")"
    desc="$(bashio::config "devices[${i}].description")"
    haid="$(bashio::config "devices[${i}].haid")"

    # bashio yields "null" for unset optional fields; normalise to empty.
    [ "${ctype}" = "null" ] && ctype=""
    [ "${psk}" = "null" ] && psk=""
    [ "${iv}" = "null" ] && iv=""
    [ "${desc}" = "null" ] && desc=""
    [ "${haid}" = "null" ] && haid=""

    # Auto-fill from the parsed ZIP inventory by haId (operator values win).
    if [ -n "${haid}" ]; then
      [ -z "${ctype}" ] && ctype="$(inv_get "${haid}" connectionType)"
      [ -z "${psk}" ] && psk="$(inv_get "${haid}" psk64)"
      [ -z "${iv}" ] && iv="$(inv_get "${haid}" iv64)"
      [ -z "${desc}" ] && desc="${PROFILES}/${haid}.json"
    fi

    {
      echo "  - name: \"${name}\""
      echo "    host: \"${host}\""
      if [ -n "${ctype}" ]; then echo "    connection_type: ${ctype}"; fi
      echo "    psk64: \"${psk}\""
    } >> "${DEVICES}"
    if [ -n "${iv}" ]; then echo "    iv64: \"${iv}\"" >> "${DEVICES}"; fi
    if [ -n "${desc}" ]; then echo "    description: \"${desc}\"" >> "${DEVICES}"; fi
  done
else
  bashio::log.warning "No devices configured under 'devices'."
  echo "devices: []" > "${DEVICES}"
fi

# Minimal config file; all scalar settings come from the HC2M_* env above.
echo "{}" > /data/config.yaml

bashio::log.info "Configuration prepared; MQTT ${HC2M_MQTT_SERVER}, topic ${HC2M_MQTT_TOPIC}."
bashio::log.info "Web UI bound to ${HC2M_WEB_BIND} (served via Ingress)."

# Hand off to the daemon (becomes PID 1). The enrichment catalogue
# (mapping.yaml) is resolved from the WORKDIR (/app) set in the Dockerfile.
exec /usr/bin/homeconnect2mqtt --config /data/config.yaml --devices "${DEVICES}"
