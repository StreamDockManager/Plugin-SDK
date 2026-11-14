declare module 'pngjs' {
    export const PNG: {
        sync: {
            // Encode RGBA pixels as PNG bytes
            write(
                image: { width: number; height: number; data: Uint8Array },
                options?: unknown,
            ): Uint8Array;
            // Decode PNG bytes into dimensions and pixel data
            read(
                data: Uint8Array,
                options?: unknown,
            ): { width: number; height: number; data: Uint8Array };
        };
    };
}
declare module 'omggif' {
    export class GifReader {
        // Open encoded GIF bytes for frame metadata and decoding
        constructor(data: Uint8Array);
        width: number;
        height: number;
        // Return the dimensions and position of a GIF frame
        frameInfo(index: number): { x: number; y: number; width: number; height: number };
        // Composite a decoded GIF frame into the supplied RGBA buffer
        decodeAndBlitFrameRGBA(index: number, output: Uint8Array): void;
    }
}
