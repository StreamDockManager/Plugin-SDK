import { run } from 'stream-dock-manager-plugin-sdk';
import { counter } from './counter.mjs';
// Run the shared counter implementation using the host launch arguments
await run(counter());
