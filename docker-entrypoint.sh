#!/bin/sh
set -eu

if [ "$(id -u)" = "0" ]; then
  mkdir -p /data/vault /data/state
	for directory in /data /data/vault /data/state; do
		if [ "$(stat -c %u "$directory")" != "10001" ]; then
			chown symdesk:symdesk "$directory"
		fi
	done
  if [ -f /data/options.json ]; then
    chgrp symdesk /data/options.json
    chmod g+r /data/options.json
  fi
fi

case "${1:-}" in
  serve|worker|mcp|version|doctor)
    set -- symdesk "$@"
    ;;
esac

if [ "$(id -u)" = "0" ]; then
  exec gosu symdesk "$@"
fi
exec "$@"
