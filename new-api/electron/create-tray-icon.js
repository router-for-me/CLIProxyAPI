// Create a simple tray icon for macOS
// Run: node create-tray-icon.js

const fs = require('fs');
const { createCanvas } = require('canvas');

function createTrayIcon() {
  // For macOS, we'll use a Template image (black and white)
  // Size should be 22x22 for Retina displays (@2x would be 44x44)
  const canvas = createCanvas(22, 22);
  const ctx = canvas.getContext('2d');

  // Clear canvas
  ctx.clearRect(0, 0, 22, 22);

  // Draw a simple "API" icon
  ctx.fillStyle = '#000000';
  ctx.font = 'bold 10px system-ui';
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  ctx.fillText('API', 11, 11);

  // Save as PNG
  const buffer = canvas.toBuffer('image/png');
  fs.writeFileSync('tray-icon.png', buffer);

  // For Template images on macOS (will adapt to menu bar theme)
  fs.writeFileSync('tray-iconTemplate.png', buffer);
  fs.writeFileSync('tray-iconTemplate@2x.png', buffer);

  console.log('Tray icon created successfully!');
}

// Check if canvas is installed
try {
  createTrayIcon();
} catch (err) {
  console.log('Canvas module not installed.');
  console.log('For now, creating a placeholder. Install canvas with: npm install canvas');

  // Create a minimal 1x1 transparent PNG as placeholder
  const minimalPNG = Buffer.from([
    0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
    0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
    0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
    0x01, 0x03, 0x00, 0x00, 0x00, 0x25, 0xDB, 0x56,
    0xCA, 0x00, 0x00, 0x00, 0x03, 0x50, 0x4C, 0x54,
    0x45, 0x00, 0x00, 0x00, 0xA7, 0x7A, 0x3D, 0xDA,
    0x00, 0x00, 0x00, 0x01, 0x74, 0x52, 0x4E, 0x53,
    0x00, 0x40, 0xE6, 0xD8, 0x66, 0x00, 0x00, 0x00,
    0x0A, 0x49, 0x44, 0x41, 0x54, 0x08, 0x1D, 0x62,
    0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00,
    0x00, 0x01, 0x0A, 0x2D, 0xCB, 0x59, 0x00, 0x00,
    0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42,
    0x60, 0x82
  ]);

  fs.writeFileSync('tray-icon.png', minimalPNG);
  console.log('Created placeholder tray icon.');
}