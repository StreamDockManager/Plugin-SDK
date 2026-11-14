/** Explicit image-byte encoding; no file/URL reads or permission changes. */
export const MAX_PORTABLE_IMAGE_BYTES = 512 * 1024;
/** Encode trusted PNG/SVG bytes, not paths. Does not decode or sanitize images.
 * The complete outgoing protocol message limit still applies. */
export function imageDataURI(mediaType: string, data: Uint8Array): string {
    if (mediaType !== 'image/png' && mediaType !== 'image/svg+xml')
        throw new Error('portable image media type must be image/png or image/svg+xml');
    if (!(data instanceof Uint8Array))
        throw new TypeError('portable image data must be Uint8Array');
    if (data.byteLength < 1 || data.byteLength > MAX_PORTABLE_IMAGE_BYTES)
        throw new Error('portable image must contain 1..524288 bytes');
    return `data:${mediaType};base64,${Buffer.from(data).toString('base64')}`;
}
