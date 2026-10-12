#!/bin/sh
set -eu
repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "$(uname -s)" in
  Darwin)
    cipher_prefix=${SQLCIPHER_PREFIX:-$(brew --prefix sqlcipher)}
    cipher_library="$cipher_prefix/lib/libsqlcipher.dylib"
    ;;
  Linux)
    cipher_prefix=${SQLCIPHER_PREFIX:-/opt/sqlcipher}
    cipher_library="$cipher_prefix/lib/libsqlite3.so"
    export LD_LIBRARY_PATH="$cipher_prefix/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
    ;;
  *) echo "Supported build platforms: macOS and Linux" >&2; exit 1 ;;
esac
if [ ! -f "$cipher_library" ]; then
  echo "SQLCipher shared library missing; install SQLCipher 4 or set SQLCIPHER_PREFIX" >&2
  exit 1
fi
link_directory="$repository_dir/.build/sqlcipher"
mkdir -p "$link_directory"
# The maintained driver uses -lsqlite3. Resolve that name only to SQLCipher.
case "$(uname -s)" in
  Darwin) ln -sf "$cipher_library" "$link_directory/libsqlite3.dylib" ;;
  Linux) ln -sf "$cipher_library" "$link_directory/libsqlite3.so" ;;
esac
export CGO_ENABLED=1
export CGO_CFLAGS="-I$cipher_prefix/include -I$cipher_prefix/include/sqlcipher ${CGO_CFLAGS:-}"
export CGO_LDFLAGS="-L$link_directory -L$cipher_prefix/lib ${CGO_LDFLAGS:-}"
cd "$repository_dir/backend"
command=$1
shift
go "$command" -tags libsqlite3 "$@"
