#!/bin/sh
set -eu
key_path=${1:-.local/secrets/database.key}
umask 077
mkdir -p "$(dirname "$key_path")"
# Noclobber prevents replacing the key of an existing database.
(set -C; openssl rand -hex 32 > "$key_path")
chmod 600 "$key_path"
echo "Encryption key created at $key_path"
