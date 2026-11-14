import { Renderer } from '../dist/src/authoring.js';
import { Recorder } from '../dist/src/testkit.js';
import process from 'node:process';
const [frames, surfaces] = process.argv.slice(2).map(Number);
if (
    !Number.isInteger(frames) ||
    frames < 1 ||
    frames > 1200 ||
    !Number.isInteger(surfaces) ||
    surfaces < 1 ||
    surfaces > 14
)
    throw Error('Workload outside bounds');
const renderer = new Renderer(),
    recorder = new Recorder();
let count = 0;
const started = performance.now();
for (let frame = 0; frame < frames; frame++)
    for (let surface = 0; surface < surfaces; surface++) {
        for (let duplicate = 0; duplicate < 2; duplicate++)
            renderer.render(recorder.client, 'fixture-' + surface, { title: String(frame) });
        const events = recorder.drain();
        if (events.length !== (frame === 0 ? 2 : 1))
            throw Error('Unexpected or duplicate display events');
        count += events.length;
    }
console.log(
    JSON.stringify({
        schema_version: 1,
        measurement_scope: 'sdk_authoring_inprocess',
        implementation: 'javascript',
        frames,
        surfaces,
        received_events: count,
        display_revisions: frames * surfaces,
        elapsed_ms: performance.now() - started,
    }),
);
recorder.close();
