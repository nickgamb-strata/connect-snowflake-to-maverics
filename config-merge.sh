#!/bin/sh
# Smart Maverics config merge: objects deep-merge, named arrays merge by 'name' field.
set -eu

BASE="/config/base.yaml"
OVERLAY="/config/overlay.yaml"
OUTPUT="/config/merged/maverics.yaml"

mkdir -p "$(dirname "$OUTPUT")" 2>/dev/null || true

if [ ! -f "$OVERLAY" ]; then
    echo "==> No overlay found, using base config."
    cp "$BASE" "$OUTPUT"
    exit 0
fi

echo "==> Merging base config with overlay..."
yq eval-all 'select(fileIndex == 0) * select(fileIndex == 1)' "$BASE" "$OVERLAY" > "$OUTPUT"

for FIELD in connectors apps apis; do
    BASE_ARRAY=$(yq -o=json "(.${FIELD} // [])" "$BASE")
    yq -i ".${FIELD} = ${BASE_ARRAY}" "$OUTPUT"
done

for FIELD in connectors apps apis; do
    COUNT=$(yq "(.${FIELD} // []) | length" "$OVERLAY")
    [ "$COUNT" -eq 0 ] && continue

    i=0
    while [ "$i" -lt "$COUNT" ]; do
        NAME=$(yq ".${FIELD}[$i].name" "$OVERLAY")
        BASE_IDX=$(yq "(.${FIELD} // []) | to_entries | .[] | select(.value.name == \"${NAME}\") | .key" "$BASE")
        ITEM=$(yq -o=json ".${FIELD}[$i]" "$OVERLAY")

        if [ -n "$BASE_IDX" ]; then
            echo "  Replacing: ${FIELD}[${NAME}]"
            yq -i ".${FIELD}[$BASE_IDX] = ${ITEM}" "$OUTPUT"
        else
            echo "  Appending: ${FIELD}[${NAME}]"
            yq -i ".${FIELD} += [${ITEM}]" "$OUTPUT"
        fi
        i=$((i + 1))
    done
done

echo "==> Merge complete."
