#!/usr/bin/env bash
# Rebuilds testdata/apps/hellojar/hello.jar (a few KB, committed) with a JDK
# in Docker, so no local Java is needed. Only needed when Hello.java changes.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
docker run --rm -v "$DIR":/work -w /work eclipse-temurin:21-jdk sh -c '
  rm -rf /tmp/out && mkdir /tmp/out &&
  javac --release 21 -d /tmp/out src/Hello.java &&
  printf "Main-Class: Hello\n" > /tmp/manifest.txt &&
  jar --create --file hello.jar --manifest /tmp/manifest.txt --date 2026-01-01T00:00:00Z -C /tmp/out . &&
  chmod a+r hello.jar'
ls -l "$DIR/hello.jar"
