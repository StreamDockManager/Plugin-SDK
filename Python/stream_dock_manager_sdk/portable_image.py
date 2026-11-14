"""Explicit image-byte encoding; never reads files/URLs or changes permissions."""
import base64

MAX_PORTABLE_IMAGE_BYTES = 512 * 1024


# Encode trusted PNG/SVG bytes, not paths. Does not decode or sanitize images
def image_data_uri(media_type: str, data: bytes) -> str:
    """Encode trusted PNG/SVG bytes, not paths. Does not decode or sanitize images.

    The byte bound leaves room for base64 expansion and a normal envelope; the
    complete outgoing protocol message limit still applies.
    """
    if media_type not in ('image/png', 'image/svg+xml'):
        raise ValueError('portable image media type must be image/png or image/svg+xml')
    if not isinstance(data, bytes):
        raise TypeError('portable image data must be bytes')
    if not 1 <= len(data) <= MAX_PORTABLE_IMAGE_BYTES:
        raise ValueError('portable image must contain 1..524288 bytes')
    return f'data:{media_type};base64,' + base64.b64encode(data).decode('ascii')
