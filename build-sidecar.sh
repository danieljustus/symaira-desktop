#!/bin/bash
TARGET=$(rustc -vV | sed -n 's|host: ||p')
mkdir -p ui/src-tauri/bin
go build -o ui/src-tauri/bin/symdesk-$TARGET ./cmd/symdesk
