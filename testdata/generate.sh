#!/bin/bash

# Generates a 5000-file test vault

VAULT_DIR="5000-vault"
mkdir -p "$VAULT_DIR"

echo "Generating 5000 markdown files in $VAULT_DIR..."

for i in {1..5000}; do
    # random status
    STATUS=$((i % 3))
    if [ $STATUS -eq 0 ]; then
        STATUS_STR="offen"
    elif [ $STATUS -eq 1 ]; then
        STATUS_STR="in_bearbeitung"
    else
        STATUS_STR="bezahlt"
    fi

    # Link to a random previous note (if i > 1)
    LINK=""
    if [ $i -gt 1 ]; then
        TARGET=$(( (RANDOM % (i-1)) + 1 ))
        LINK="[[Note_$TARGET]]"
    fi

    cat <<EOF > "$VAULT_DIR/Note_$i.md"
---
status: $STATUS_STR
type: rechnung
amount: $((RANDOM % 1000)).$((RANDOM % 99))
tags:
  - test
  - generiert
---
# Note $i

Dies ist eine automatisch generierte Notiz für das Performance-Gate.

$LINK
EOF
done

echo "Done."
