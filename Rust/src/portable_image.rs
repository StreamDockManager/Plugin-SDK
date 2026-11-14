//! Explicit byte encoding for portable setImage messages. No filesystem or network I/O
use base64::Engine;
/// Leaves room for base64 expansion and the ordinary protocol envelope
pub const MAX_PORTABLE_IMAGE_BYTES: usize = 512 * 1024;
/// Encode trusted, caller-provided PNG/SVG bytes. This does not decode or sanitize
/// images, change paths, or grant permissions. Complete message limits still apply
pub fn image_data_uri(media_type: &str, data: &[u8]) -> crate::Result<String> {
    if !matches!(media_type, "image/png" | "image/svg+xml") {
        return Err(crate::error(
            "portable image media type must be image/png or image/svg+xml",
        ));
    }
    if data.is_empty() || data.len() > MAX_PORTABLE_IMAGE_BYTES {
        return Err(crate::error("portable image must contain 1..524288 bytes"));
    }
    Ok(format!(
        "data:{media_type};base64,{}",
        base64::engine::general_purpose::STANDARD.encode(data)
    ))
}
