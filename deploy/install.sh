#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo 'Run as root on the target server' >&2
  exit 1
fi

root_dir=/opt/flashlarry
source_dir=$root_dir/source
domain=${OPENXGQL_DOMAIN:-flashlarry.duckdns.org}
if [[ ! $domain =~ ^[A-Za-z0-9][A-Za-z0-9.-]*[A-Za-z0-9]$ ]]; then
  echo 'OPENXGQL_DOMAIN must be a DNS hostname' >&2
  exit 1
fi

apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql redis-server caddy openssl curl python3
systemctl enable --now postgresql redis-server

if ! id flashlarry >/dev/null 2>&1; then
  useradd --system --user-group --no-create-home --shell /usr/sbin/nologin flashlarry
fi
if ! runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='flashlarry'" | grep -q 1; then
  runuser -u postgres -- psql -c 'CREATE ROLE flashlarry LOGIN'
fi
if ! runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='flashlarry'" | grep -q 1; then
  runuser -u postgres -- createdb -O flashlarry flashlarry
fi

install -d -m 0755 "$root_dir/bin"
install -d -m 0750 -o flashlarry -g flashlarry "$root_dir/media"
install -d -m 0750 -o root -g flashlarry /etc/flashlarry
if [[ ! -f /etc/flashlarry/common.env ]]; then
  printf '%s\n' 'DATABASE_URL=postgres:///flashlarry?host=/var/run/postgresql&sslmode=disable' 'REDIS_ADDR=127.0.0.1:6379' 'USERS_URL=http://127.0.0.1:8081' 'TWEETS_URL=http://127.0.0.1:8082' > /etc/flashlarry/common.env
fi
chmod 0640 /etc/flashlarry/common.env
chown root:flashlarry /etc/flashlarry/common.env
if ! grep -q '^PUBLIC_BASE_URL=' /etc/flashlarry/common.env; then
  printf 'PUBLIC_BASE_URL=https://%s\n' "$domain" >> /etc/flashlarry/common.env
fi
if ! grep -q '^SOCIAL_INTERNAL_TOKEN=' /etc/flashlarry/common.env; then
  printf 'SOCIAL_INTERNAL_TOKEN=%s\nSOCIAL_URL=http://127.0.0.1:8083\n' "$(openssl rand -hex 32)" >> /etc/flashlarry/common.env
fi
if ! grep -q '^SETTINGS_ENCRYPTION_KEY=' /etc/flashlarry/common.env; then
  printf 'SETTINGS_ENCRYPTION_KEY=%s\n' "$(openssl rand -base64 32)" >> /etc/flashlarry/common.env
fi
if [[ ! -f /etc/flashlarry/admin.env ]]; then
  PUBLIC_BASE_URL="https://$domain" python3 "$source_dir/scripts/generate_admin_credentials.py" /root/flashlarry-admin-credentials.txt /etc/flashlarry/admin.env
fi
chmod 0640 /etc/flashlarry/admin.env
chown root:flashlarry /etc/flashlarry/admin.env
python3 - <<'PYKEY'
import secrets,subprocess
r=subprocess.run(['redis-cli','EXISTS','fl:media-signing-key'],capture_output=True,text=True,check=True)
if r.stdout.strip()=='0':
    subprocess.run(['redis-cli','-x','SET','fl:media-signing-key'],input=secrets.token_bytes(32),capture_output=True,check=True)
PYKEY
for service in users tweets social gateway admin bot; do
  install -m 0755 "$root_dir/staging/$service" "$root_dir/bin/$service.new"
  mv -f "$root_dir/bin/$service.new" "$root_dir/bin/$service"
  install -m 0644 "$source_dir/deploy/flashlarry-$service.service" "/etc/systemd/system/flashlarry-$service.service"
done
install -m 0755 "$source_dir/deploy/flashlarryctl" /usr/local/bin/flashlarryctl
install -d -m 0700 -o caddy -g caddy /var/log/caddy

if [[ -f /etc/caddy/Caddyfile && ! -f /etc/caddy/Caddyfile.before-flashlarry ]]; then
  cp /etc/caddy/Caddyfile /etc/caddy/Caddyfile.before-flashlarry
fi
sed "s/flashlarry.duckdns.org/$domain/g" "$source_dir/deploy/Caddyfile" > /etc/caddy/Caddyfile
chmod 0644 /etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile
systemctl daemon-reload
systemctl enable --now flashlarry-users flashlarry-tweets flashlarry-social flashlarry-gateway flashlarry-admin caddy
systemctl restart flashlarry-users flashlarry-tweets flashlarry-social flashlarry-gateway flashlarry-admin
if grep -q '^TELEGRAM_BOT_TOKEN=' /etc/flashlarry/common.env; then
  systemctl enable --now flashlarry-bot
  systemctl restart flashlarry-bot
fi
systemctl reload caddy

if [[ ! -f /etc/flashlarry/operator.json ]]; then
  password=$(openssl rand -hex 24)
  for attempt in $(seq 1 20); do
    if curl -fsS http://127.0.0.1:8081/healthz >/dev/null 2>&1; then break; fi
    sleep 1
  done
  payload=$(python3 -c 'import json,sys; print(json.dumps({"screen_name":"flashlarry","name":"Flashlarry Operator","password":sys.argv[1]}))' "$password")
  response=$(curl -fsS -H 'Content-Type: application/json' --data "$payload" http://127.0.0.1:8081/auth/register)
  printf '{"password":"%s","auth":%s}\n' "$password" "$response" > /etc/flashlarry/operator.json
  chmod 0600 /etc/flashlarry/operator.json
fi

echo 'Flashlarry deployed. Operator credentials are stored in /etc/flashlarry/operator.json'
