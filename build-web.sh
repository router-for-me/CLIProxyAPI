#!/usr/bin/env bash
set -e

echo "Building Next.js frontend..."
cd web
npm run build
cd ..

echo "Copying static export to embed directory..."
dest="internal/managementasset/web_static"
rm -rf "$dest"
mkdir -p "$dest"
cp -r web/out/* "$dest/"

echo "Done! Web assets embedded."
