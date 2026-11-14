import { finiteJSONValue } from './internal/json.js';
import { byteLength } from './internal/bytes.js';
import { mkdir, open } from 'node:fs/promises';
import { homedir, platform } from 'node:os';
import process from 'node:process';
import { dirname, join } from 'node:path';
import { atomicWrite } from './internal/atomic-write.js';
import { MAXIMUM_STATE_BYTES, readState, missingParent } from './internal/state-read.js';
const stateJSONValue = finiteJSONValue('Invalid state JSON number');
// Validate the persisted state version
function stateVersion(version: number): void {
    if (!Number.isInteger(version) || version !== 1) throw Error('Invalid state version');
}
// Persist the serialized state atomically
export async function write(path: string, value: any): Promise<void> {
    const data = JSON.stringify(value, stateJSONValue);
    if (byteLength(data) > MAXIMUM_STATE_BYTES) throw Error('State exceeds limit');
    await mkdir(dirname(path), { recursive: true });
    return atomicWrite(path, data, true);
}
// Resolve the state file for the requested namespace
export function pathFor(root: string, identity: string): string {
    if (!/^[A-Za-z0-9._-]{1,180}$/.test(identity) || identity.includes('..'))
        throw Error('Invalid plugin identity');
    return join(root, 'stream-dock-manager-plugins', identity, 'state.json');
}
export class Store {
    #loadBlocked = false;
    value: any = {};
    dirty = false;
    due = 0;
    // Bind the local state file to its version and write policy
    constructor(
        readonly path: string,
        readonly version = 1,
        readonly clock = Date.now,
    ) {
        stateVersion(version);
    }
    // Load and validate the state file within its size limit
    async load(): Promise<any> {
        this.#loadBlocked = true;
        stateVersion(this.version);
        let text;
        try {
            const file = await open(this.path, 'r');
            try {
                text = (await readState(file)).toString('utf8');
            } finally {
                await file.close();
            }
        } catch (error) {
            if ((error as NodeJS.ErrnoException).code === 'ENOENT') {
                await missingParent(this.path);
                this.#loadBlocked = false;
                return this.value;
            }
            throw error;
        }
        const raw = JSON.parse(text, stateJSONValue);
        if (!raw || typeof raw !== 'object' || raw.version !== 1 || !Object.hasOwn(raw, 'data'))
            throw Error('Unsupported state format: version 1 envelope required');
        const value = raw.data;
        const loaded = structuredClone(value);
        this.value = value;
        this.#loadBlocked = false;
        return loaded;
    }
    // Apply an in-memory state change
    change(value: any): void {
        this.value = structuredClone(value);
        this.dirty = true;
        if (!this.due) this.due = this.clock() + 250;
    }
    // Persist pending state changes
    async flush(force = false): Promise<boolean> {
        if (this.#loadBlocked)
            throw Error('saved state is protected: repair or move it and reload before writing');
        stateVersion(this.version);
        if (!this.dirty || (!force && this.clock() < this.due)) return false;
        await write(this.path, { version: this.version, data: this.value });
        this.dirty = false;
        this.due = 0;
        return true;
    }
    // Flush owned state and close the store
    close(): Promise<boolean> {
        return this.flush(true);
    }
}

// Choose the platform-specific default state directory
export function defaultPath(identity: string): string {
    const root =
        process.env.STREAM_DOCK_MANAGER_PLUGIN_STATE_DIR ||
        (platform() === 'win32'
            ? process.env.APPDATA
            : platform() === 'darwin'
              ? join(homedir(), 'Library/Application Support')
              : process.env.XDG_CONFIG_HOME || join(homedir(), '.config'));
    if (!root) throw Error('User configuration directory unavailable');
    return pathFor(root, identity);
}
