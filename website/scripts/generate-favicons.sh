#!/bin/sh
# Regenerate the favicon set from website/static/favicon.svg.
#
# Requires rsvg-convert and ImageMagick (magick). The SVG has a transparent
# background and switches to the white mark under prefers-color-scheme: dark.
set -eu
cd "$(dirname "$0")/.."

static=static
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

rsvg-convert -w 512 -h 512 -o "$tmp/favicon-512.png" "$static/favicon.svg"

for size in 16 32 48 64 96 128 192 512; do
	magick "$tmp/favicon-512.png" -resize "${size}x${size}" -strip \
		"PNG32:$static/favicon-${size}x${size}.png"
done

magick "$tmp/favicon-512.png" -resize 180x180 -strip \
	"PNG32:$static/apple-touch-icon.png"

# Keep the mark inside the maskable safe zone (80% of the canvas).
magick "$tmp/favicon-512.png" -background none -gravity center \
	-extent 640x640 -resize 512x512 -strip \
	"PNG32:$static/favicon-maskable-512x512.png"

magick "$static/favicon-16x16.png" "$static/favicon-32x32.png" \
	"$static/favicon-48x48.png" "$static/favicon.ico"

printf 'wrote favicons under %s\n' "$static"
