#!/bin/sh
# Make the private CA and a client certificate for mTLS (PRD Fase 9c).
#
# Run this on the LAPTOP (or an offline machine), never on the VPS: the CA
# private key (ca.key) must not leave this machine. Needs openssl.
#
#   scripts/mtls-init.sh [-d DIR] [-n NAME] [--force]
#
#   -d DIR    where the files go (default ./tunas-mtls, created with mode 700)
#   -n NAME   device name, [a-z0-9-]+ (default laptop)
#   --force   replace an existing client certificate of that name
#
# First run: creates the CA (ca.key, ca.pem; 10 years) and the client
# certificate for NAME (NAME.key, NAME.crt; 365 days, key usage clientAuth).
# Later runs with another NAME reuse the CA, so a second device needs no
# change on the VPS. Environment: CLIENT_DAYS (365), CA_DAYS (3650).
set -eu

dir=./tunas-mtls
name=laptop
force=0
while [ $# -gt 0 ]; do
  case "$1" in
    -d) [ $# -ge 2 ] || { echo "-d needs a directory" >&2; exit 2; }; dir=$2; shift 2 ;;
    -n) [ $# -ge 2 ] || { echo "-n needs a name" >&2; exit 2; }; name=$2; shift 2 ;;
    --force) force=1; shift ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
  esac
done
case "$name" in
  ""|*[!a-z0-9-]*) echo "name must match [a-z0-9-]+" >&2; exit 2 ;;
esac
command -v openssl >/dev/null 2>&1 || { echo "openssl is required" >&2; exit 1; }

CLIENT_DAYS=${CLIENT_DAYS:-365}
CA_DAYS=${CA_DAYS:-3650}

umask 077
mkdir -p "$dir"
chmod 700 "$dir" 2>/dev/null || true
tmp=$(mktemp -d "$dir/.work.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

# ---- the CA (once) ------------------------------------------------------------
if [ -f "$dir/ca.key" ] && [ -f "$dir/ca.pem" ]; then
  echo "using the existing CA in $dir"
elif [ -f "$dir/ca.key" ] || [ -f "$dir/ca.pem" ]; then
  echo "$dir has only one of ca.key / ca.pem: refusing to guess, fix or remove it" >&2
  exit 1
else
  cat > "$tmp/ca.cnf" <<'CNF'
[req]
distinguished_name = dn
x509_extensions = v3_ca
prompt = no
[dn]
CN = Tunas private CA
[v3_ca]
basicConstraints = critical, CA:TRUE, pathlen:0
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
CNF
  openssl ecparam -name prime256v1 -genkey -noout -out "$dir/ca.key"
  openssl req -x509 -new -key "$dir/ca.key" -sha256 -days "$CA_DAYS" -config "$tmp/ca.cnf" -out "$dir/ca.pem"
  chmod 600 "$dir/ca.key"
  echo "created the CA: $dir/ca.pem (public) and $dir/ca.key (PRIVATE, keep it here)"
fi

# ---- the client certificate -----------------------------------------------------
key=$dir/$name.key
crt=$dir/$name.crt
if [ -e "$crt" ] || [ -e "$key" ]; then
  [ "$force" = 1 ] || { echo "$crt or $key exists; use --force to replace it" >&2; exit 1; }
fi
cat > "$tmp/client.ext" <<'EXT'
basicConstraints = critical, CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = clientAuth
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid
EXT
openssl ecparam -name prime256v1 -genkey -noout -out "$tmp/client.key"
printf "[req]
distinguished_name = dn
prompt = no
[dn]
CN = tunas-%s
" "$name" > "$tmp/client.cnf"
openssl req -new -key "$tmp/client.key" -config "$tmp/client.cnf" -out "$tmp/client.csr"
openssl x509 -req -in "$tmp/client.csr" -CA "$dir/ca.pem" -CAkey "$dir/ca.key" -CAcreateserial \
  -CAserial "$tmp/ca.srl" -days "$CLIENT_DAYS" -sha256 -extfile "$tmp/client.ext" -out "$tmp/client.crt" 2>/dev/null
# PKCS#8 PEM ("BEGIN PRIVATE KEY"), which the agent reads
openssl pkcs8 -topk8 -nocrypt -in "$tmp/client.key" -out "$key"
cp "$tmp/client.crt" "$crt"
chmod 600 "$key"

# ---- self-check: the chain verifies for client authentication --------------------
openssl verify -CAfile "$dir/ca.pem" -purpose sslclient "$crt" >/dev/null || { echo "self-check failed: the certificate does not verify" >&2; exit 1; }
openssl x509 -in "$crt" -noout -text | grep -q "TLS Web Client Authentication" || { echo "self-check failed: no clientAuth key usage" >&2; exit 1; }
echo "created the client certificate for $name: $crt and $key (valid $CLIENT_DAYS days, until $(openssl x509 -in "$crt" -noout -enddate | cut -d= -f2))"

cat <<EOF

Next steps (details: docs/DEPLOY.md, Fase 9c):
  1. VPS:    copy ONLY the public CA to the VPS:  scp $dir/ca.pem user@vps:/etc/caddy/tunas-ca.pem
             (never copy ca.key; it stays on this machine, offline if you can)
  2. Laptop: put $crt and $key under %APPDATA%\\tunas\\ and set in config.yaml:
               tls:
                 client_cert: C:\\Users\\<you>\\AppData\\Roaming\\tunas\\$name.crt
             then store the key encrypted and delete the plain file:
               tunas-agent.exe --set-client-key $key
  3. Renew before the certificate expires (the tray turns yellow 30 days before):
             scripts/mtls-init.sh -d $dir -n $name --force
EOF
