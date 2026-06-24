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
  if bashio::services 'mqtt' 'ssl'; then
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

# --- Optional: parse the Home Connect profile ZIP into device descriptions ---
PROFILES="/data/profiles"
mkdir -p "${PROFILES}"
if bashio::config.has_value 'profile_zip'; then
  ZIP="$(bashio::config 'profile_zip')"
  if [ -f "${ZIP}" ]; then
    bashio::log.info "Parsing profile archive ${ZIP} -> ${PROFILES}"
    if ! hc-util parse "${ZIP}" --out "${PROFILES}"; then
      bashio::log.warning "hc-util parse failed; relying on explicit 'description' paths."
    fi
  else
    bashio::log.warning "profile_zip '${ZIP}' not found (is /share mapped and the path correct?)."
  fi
fi

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

    if [ -z "${desc}" ] || [ "${desc}" = "null" ]; then
      desc=""
      if [ -n "${haid}" ] && [ "${haid}" != "null" ]; then
        desc="${PROFILES}/${haid}.json"
      fi
    fi

    {
      echo "  - name: \"${name}\""
      echo "    host: \"${host}\""
      echo "    connection_type: ${ctype}"
      echo "    psk64: \"${psk}\""
    } >> "${DEVICES}"
    if [ -n "${iv}" ] && [ "${iv}" != "null" ]; then
      echo "    iv64: \"${iv}\"" >> "${DEVICES}"
    fi
    if [ -n "${desc}" ]; then
      echo "    description: \"${desc}\"" >> "${DEVICES}"
    fi
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
