/** Optional pure-JavaScript codecs. Run decode in an SDK WorkerPool for CPU isolation
 * and interruptible work; a cancelled worker never publishes a partial thumbnail. */
import jpeg from 'jpeg-js';
import { PNG } from 'pngjs';
import { GifReader } from 'omggif';
import { protectPngCall } from './worker-cancellation.js';
export const MAX_INPUT_BYTES = 64 << 20,
    MAX_SOURCE_PIXELS = 32000000,
    MAX_DIMENSION = 320;
export interface Limits {
    maxInputBytes?: number;
    maxSourcePixels?: number;
}
// Resolve and validate thumbnail resource limits
function limits(options: Limits): Required<Limits> {
    const value = {
        maxInputBytes: options.maxInputBytes ?? MAX_INPUT_BYTES,
        maxSourcePixels: options.maxSourcePixels ?? MAX_SOURCE_PIXELS,
    };
    if (!Object.values(value).every((v) => Number.isSafeInteger(v) && v > 0))
        throw Error('Invalid thumbnail limits');
    return value;
}
export interface Thumbnail {
    width: number;
    height: number;
    data: Uint8Array;
}
// Reject image dimensions outside the decode budget
const dimensions = (width: number, height: number, maximum: number) => {
    if (width < 1 || height < 1 || width > maximum / height)
        throw Error('Invalid thumbnail source dimensions');
};
// Keep only the first GIF frame before passing to a parser that indexes frames
function firstGIF(data: Uint8Array): Uint8Array {
    if (data.length < 13) throw Error('Truncated GIF');
    let at = 13 + (data[10] & 128 ? 3 * (1 << ((data[10] & 7) + 1)) : 0);
    // Check image blocks before allocating decoded storage
    const blocks = () => {
        while (true) {
            if (at >= data.length) throw Error('Truncated GIF');
            const size = data[at++];
            if (!size) return;
            at += size;
            if (at > data.length) throw Error('Truncated GIF');
        }
    };
    while (at < data.length) {
        const kind = data[at++];
        if (kind === 0x21) {
            at++;
            blocks();
            continue;
        }
        if (kind !== 0x2c || at + 9 > data.length) throw Error('Invalid GIF frame');
        const flags = data[at + 8];
        at += 9;
        if (flags & 128) at += 3 * (1 << ((flags & 7) + 1));
        at++;
        blocks();
        const first = new Uint8Array(at + 1);
        first.set(data.subarray(0, at));
        first[at] = 0x3b;
        return first;
    }
    throw Error('Missing GIF frame');
}
// Decode supported image bytes within the configured limits
function decodeImage(
    data: Uint8Array,
    width: number,
    height: number,
    cancelled: () => boolean = () => false,
    fitWithin = false,
    options: Limits = {},
): Thumbnail {
    const bound = limits(options);
    // Validate an image codec result before use
    const check = () => {
        if (cancelled()) throw Error('Thumbnail cancelled');
    };
    check();
    if (
        !(data instanceof Uint8Array) ||
        !data.length ||
        data.length > bound.maxInputBytes ||
        !Number.isInteger(width) ||
        !Number.isInteger(height) ||
        width < 1 ||
        height < 1 ||
        width > MAX_DIMENSION ||
        height > MAX_DIMENSION
    )
        throw Error('Thumbnail input or output exceeds supported limits');
    let source: Thumbnail,
        offsetX = 0,
        offsetY = 0,
        frameWidth = 0,
        frameHeight = 0;
    const bytes = Buffer.from(data.buffer, data.byteOffset, data.byteLength);
    if (bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]))) {
        if (data.length < 33) throw Error('Truncated PNG');
        dimensions(bytes.readUInt32BE(16), bytes.readUInt32BE(20), bound.maxSourcePixels);
        check();
        source = protectPngCall(() => PNG.sync.read(bytes, { checkCRC: true }));
    } else if (bytes[0] === 255 && bytes[1] === 216) {
        // jpeg-js enforces source and allocation budgets before allocating pixel planes
        source = jpeg.decode(bytes, {
            useTArray: true,
            formatAsRGBA: true,
            tolerantDecoding: false,
            maxResolutionInMP: bound.maxSourcePixels / 1_000_000,
            maxMemoryUsageInMB: Math.max(8, Math.ceil((bound.maxSourcePixels * 32) / (1 << 20))),
        });
    } else if (
        bytes.subarray(0, 6).toString() === 'GIF87a' ||
        bytes.subarray(0, 6).toString() === 'GIF89a'
    ) {
        if (data.length < 13) throw Error('Truncated GIF');
        dimensions(bytes.readUInt16LE(6), bytes.readUInt16LE(8), bound.maxSourcePixels);
        const gif = new GifReader(firstGIF(data));
        const frame = gif.frameInfo(0);
        dimensions(frame.width, frame.height, bound.maxSourcePixels);
        if (frame.x + frame.width > gif.width || frame.y + frame.height > gif.height)
            throw Error('Invalid GIF frame bounds');
        source = {
            width: gif.width,
            height: gif.height,
            data: new Uint8Array(gif.width * gif.height * 4),
        };
        gif.decodeAndBlitFrameRGBA(0, source.data);
        offsetX = frame.x;
        offsetY = frame.y;
        frameWidth = frame.width;
        frameHeight = frame.height;
    } else throw Error('Unsupported thumbnail format');
    check();
    dimensions(source.width, source.height, bound.maxSourcePixels);
    if (fitWithin) [width, height] = fit(source.width, source.height, width, height, bound);
    const result = { width, height, data: new Uint8Array(width * height * 4) };
    for (let y = 0; y < height; y++) {
        check();
        const sy = Math.floor((y * source.height) / height);
        for (let x = 0; x < width; x++) {
            const sx = Math.floor((x * source.width) / width);
            if (frameWidth && (sx >= frameWidth || sy >= frameHeight)) continue;
            const at = ((sy + offsetY) * source.width + sx + offsetX) * 4;
            result.data.set(source.data.subarray(at, at + 4), (y * width + x) * 4);
        }
    }
    check();
    return result;
}

// Calculate dimensions that fit the requested thumbnail bounds
export function fit(
    width: number,
    height: number,
    maxWidth: number,
    maxHeight: number,
    options: Limits = {},
): [number, number] {
    const bound = limits(options);
    if (
        ![width, height, maxWidth, maxHeight].every(Number.isSafeInteger) ||
        width < 1 ||
        height < 1 ||
        width > bound.maxSourcePixels / height ||
        maxWidth < 1 ||
        maxHeight < 1 ||
        maxWidth > MAX_DIMENSION ||
        maxHeight > MAX_DIMENSION
    )
        throw Error('Invalid thumbnail fit dimensions');
    if (width <= maxWidth && height <= maxHeight) return [width, height];
    return maxWidth * height <= maxHeight * width
        ? [maxWidth, Math.max(1, Math.floor((height * maxWidth) / width))]
        : [Math.max(1, Math.floor((width * maxHeight) / height)), maxHeight];
}
// Decode the source into normalized pixel data
export function decode(
    data: Uint8Array,
    width: number,
    height: number,
    cancelled: () => boolean = () => false,
    options: Limits = {},
): Thumbnail {
    return decodeImage(data, width, height, cancelled, false, options);
}
// Decode and fit image pixels to the requested bounds
export function fitDecode(
    data: Uint8Array,
    maxWidth: number,
    maxHeight: number,
    cancelled: () => boolean = () => false,
    options: Limits = {},
): Thumbnail {
    return decodeImage(data, maxWidth, maxHeight, cancelled, true, options);
}
// Encode the normalized pixels as PNG bytes
export function png(source: Thumbnail, cancelled: () => boolean = () => false): Uint8Array {
    if (cancelled()) throw Error('Thumbnail cancelled');
    if (
        !Number.isInteger(source.width) ||
        !Number.isInteger(source.height) ||
        source.width < 1 ||
        source.height < 1 ||
        source.width > MAX_DIMENSION ||
        source.height > MAX_DIMENSION ||
        !(source.data instanceof Uint8Array) ||
        source.data.length !== source.width * source.height * 4
    )
        throw Error('Invalid thumbnail pixels');
    const result = protectPngCall(() =>
        PNG.sync.write(
            { width: source.width, height: source.height, data: Buffer.from(source.data) },
            { colorType: 6, inputColorType: 6, bitDepth: 8 },
        ),
    );
    if (cancelled()) throw Error('Thumbnail cancelled');
    return result;
}
// Encode the thumbnail as a portable data URI
export function dataURI(source: Thumbnail, cancelled: () => boolean = () => false): string {
    return 'data:image/png;base64,' + Buffer.from(png(source, cancelled)).toString('base64');
}
// Render the requested thumbnail within resource limits
export function render(
    data: Uint8Array,
    maxWidth: number,
    maxHeight: number,
    cancelled: () => boolean = () => false,
    options: Limits = {},
) {
    const image = fitDecode(data, maxWidth, maxHeight, cancelled, options);
    return { width: image.width, height: image.height, dataURI: dataURI(image, cancelled) };
}
