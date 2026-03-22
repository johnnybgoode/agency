#!/usr/bin/env node
//
// Agency statusline script — writes context/rate-limit data to
// .agency-status.json for the sidebar to read, then outputs a
// minimal statusline to stdout.
//
const fs = require('fs');

let input = '';
process.stdin.on('data', chunk => input += chunk);
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);

    // Write status file for the agency sidebar.
    const status = {
      session_id: data.session_id,
      context_window: data.context_window,
      rate_limits: data.rate_limits,
      updated_at: new Date().toISOString(),
    };
    fs.writeFileSync('.agency-status.json', JSON.stringify(status));

    // Output statusline.
    const pct = Math.floor(data.context_window?.used_percentage || 0);
    const filled = Math.floor(pct * 5 / 100);
    const bar = '▓'.repeat(filled) + '░'.repeat(5 - filled);
    const color = pct >= 80 ? '\x1b[31m' : pct >= 50 ? '\x1b[33m' : '\x1b[32m';
    const reset = '\x1b[0m';
    const model = data.model?.display_name || '';
    console.log(`[${model}] ${color}${bar} ${pct}%${reset}`);
  } catch {}
});
