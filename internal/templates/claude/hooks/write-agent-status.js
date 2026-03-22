#!/usr/bin/env node
let input = '';
process.stdin.on('data', chunk => input += chunk);
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const status = {
      session_id: data.session_id,
      context_window: data.context_window,
      rate_limits: data.rate_limits,
      updated_at: new Date().toISOString(),
    };
    require('fs').writeFileSync('.agency-status.json', JSON.stringify(status));
  } catch {}
});
