#!/usr/bin/env bash
# Serves /srv/git over HTTPS at https://git.lola.test:8443/ inside a suite's container (Plan 8
# §5.4), for a suite that needs a git server at a host named by name: remote profiles and a
# catalog's git entries are fetched only from one (the team and market suites run it).
#
# It makes a test CA and a leaf for git.lola.test in /run/e2e-tls, the tmpfs the runner mounts
# at mode 0700 for every suite: the CA is limited to git.lola.test (nameConstraints, pathlen:0),
# both are valid for one day, and the private keys are mode 0600. It prints no key material and
# trusts the CA nowhere: a step that needs it passes -e GIT_SSL_CAINFO=/run/e2e-tls/ca.pem to its
# own exec. Then it maps git.lola.test to loopback in /etc/hosts and starts a second gitserve.py,
# with --tls, on 8443, logging to /run/e2e-tls/gitserve.log, and waits until it answers.
#
# usage: tls.sh
set -euo pipefail
dir=/run/e2e-tls
umask 077

if [[ "$(stat -c %a "$dir")" != 700 ]]; then
  echo "error: $dir is not mode 0700; the runner mounts it as a tmpfs with mode=0700" >&2
  exit 1
fi

cat >"$dir/ca.cnf" <<'EOF'
[req]
distinguished_name = dn
prompt = no
x509_extensions = ca
[dn]
CN = lola e2e test CA
[ca]
basicConstraints = critical, CA:TRUE, pathlen:0
keyUsage = critical, keyCertSign, cRLSign
nameConstraints = critical, permitted;DNS:git.lola.test, excluded;IP:0.0.0.0/0.0.0.0, excluded;IP:0:0:0:0:0:0:0:0/0:0:0:0:0:0:0:0
subjectKeyIdentifier = hash
[leaf]
basicConstraints = critical, CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = DNS:git.lola.test
authorityKeyIdentifier = keyid
EOF
openssl req -x509 -config "$dir/ca.cnf" -newkey ec -pkeyopt ec_paramgen_curve:P-256 -noenc \
  -keyout "$dir/ca.key" -out "$dir/ca.pem" -days 1
openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -noenc -subj /CN=git.lola.test \
  -keyout "$dir/leaf.key" -out "$dir/leaf.csr"
openssl x509 -req -in "$dir/leaf.csr" -CA "$dir/ca.pem" -CAkey "$dir/ca.key" -set_serial 2 \
  -days 1 -extfile "$dir/ca.cnf" -extensions leaf -out "$dir/leaf.pem"
chmod 0600 "$dir/ca.key" "$dir/leaf.key"
chmod 0644 "$dir/ca.pem" "$dir/leaf.pem"
rm "$dir/leaf.csr" "$dir/ca.cnf"

echo "127.0.0.1 git.lola.test" >>/etc/hosts
setsid python3 /e2e-common/gitserve.py /srv/git 8443 --tls "$dir/leaf.pem" "$dir/leaf.key" \
  </dev/null >"$dir/gitserve.log" 2>&1 &
for _ in $(seq 50); do
  if openssl s_client -connect git.lola.test:8443 -servername git.lola.test -verify_return_error \
    -CAfile "$dir/ca.pem" </dev/null >/dev/null 2>&1; then
    echo "tls ready"
    exit 0
  fi
  sleep 0.1
done
sed 's/^/gitserve: /' "$dir/gitserve.log" >&2
echo "error: the HTTPS git server on git.lola.test:8443 did not answer within 5 s" >&2
exit 1
