#!/usr/bin/env bash
# Resolve a field kit to the address that answers right now. [MESHSAT-1182]
#
#   scripts/kit-host.sh tesseract    -> nllei01tesseract01        (house LAN)
#                                    or nllei01tesseract01-field  (Mudi 5G + WireGuard)
#
# Each kit has two names. Which one answers depends on which access point the kit
# associated with, NOT on where the kit is: the Mudi broadcasts the same SSID as the
# house, so a kit can be on the tunnel while it sits on the bench, and on the house
# LAN while it sits at the booth. Never assume, probe. This is the same ladder the
# deploy job walks (.gitlab-ci.yml, DEPLOY_ADDR_ALT_<TARGET>).
#
# Overrides:
#   KIT_HOST_TESSERACT / KIT_HOST_PARALLAX   skip the probe, use this address
#   KIT_HOST_PORT                            port to probe (default 22)
#   KIT_HOST_TIMEOUT                         seconds per probe (default 2)
#
# Exits 1 and says so on stderr when neither name answers.
set -u

kit=${1:?usage: kit-host.sh <tesseract|parallax>}
case "$kit" in
  tesseract|parallax) ;;
  *) echo "kit-host: unknown kit '$kit' (tesseract|parallax)" >&2; exit 2 ;;
esac

port=${KIT_HOST_PORT:-22}
timeout_s=${KIT_HOST_TIMEOUT:-2}

# An explicit override wins and is never probed — that is the point of it.
override_var="KIT_HOST_$(echo "$kit" | tr 'a-z' 'A-Z')"
override=${!override_var:-}
if [ -n "$override" ]; then
  echo "$override"
  exit 0
fi

for addr in "nllei01${kit}01" "nllei01${kit}01-field"; do
  if timeout "$timeout_s" bash -c "exec 3<>/dev/tcp/${addr}/${port}" 2>/dev/null; then
    echo "$addr"
    exit 0
  fi
done

echo "kit-host: ${kit} answers on neither nllei01${kit}01 nor nllei01${kit}01-field (port ${port})" >&2
exit 1
