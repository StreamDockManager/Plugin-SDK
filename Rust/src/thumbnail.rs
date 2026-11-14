//! Optional bounded JPEG/PNG/GIF thumbnail decoding. Use on a background worker
//! Cancellation is checked at I/O, sample rows, and result publication; codec CPU
//! sections run until their next read. No partial result is returned
use crate::{error, Result};
use std::{
    io::{self, BufReader, Cursor, Read, Seek, SeekFrom},
    sync::atomic::{AtomicBool, Ordering},
};
pub const MAX_INPUT_BYTES: usize = 64 << 20;
pub const MAX_SOURCE_PIXELS: u64 = 32000000;
pub const MAX_DIMENSION: u32 = 320;
#[derive(Clone, Copy, Debug)]
pub struct Limits {
    pub max_input_bytes: usize,
    pub max_source_pixels: u64,
}
impl Default for Limits {
    // Supply the default configuration without performing I/O for bounded thumbnail decoding
    fn default() -> Self {
        Self {
            max_input_bytes: MAX_INPUT_BYTES,
            max_source_pixels: MAX_SOURCE_PIXELS,
        }
    }
}
impl Limits {
    // Reject values outside the supported contract for bounded thumbnail decoding
    fn validate(self) -> Result<()> {
        if self.max_input_bytes == 0
            || self.max_source_pixels == 0
            || self.max_input_bytes as u64 > 9007199254740991
            || self.max_source_pixels > 9007199254740991
        {
            return Err(error("invalid thumbnail limits"));
        }
        Ok(())
    }
}
pub struct Thumbnail {
    pub width: u32,
    pub height: u32,
    pub data: Vec<u8>,
}
struct CancelReader<'a> {
    input: Cursor<&'a [u8]>,
    cancel: &'a AtomicBool,
}
impl Read for CancelReader<'_> {
    // Read data through the owned transport or state backend for bounded thumbnail decoding
    fn read(&mut self, out: &mut [u8]) -> io::Result<usize> {
        if self.cancel.load(Ordering::Acquire) {
            return Err(io::Error::other("thumbnail cancelled"));
        }
        self.input.read(out)
    }
}
impl Seek for CancelReader<'_> {
    // Seek within the bounded encoded-image reader
    fn seek(&mut self, position: SeekFrom) -> io::Result<u64> {
        if self.cancel.load(Ordering::Acquire) {
            return Err(io::Error::other("thumbnail cancelled"));
        }
        self.input.seek(position)
    }
}
// Decode image pixels while enforcing resource limits
fn decode_image(
    data: &[u8],
    mut width: u32,
    mut height: u32,
    cancel: &AtomicBool,
    fit_within: bool,
    bounds: Limits,
) -> Result<Thumbnail> {
    bounds.validate()?;
    let check = || {
        if cancel.load(Ordering::Acquire) {
            Err(error("thumbnail cancelled"))
        } else {
            Ok(())
        }
    };
    check()?;
    if data.is_empty()
        || data.len() > bounds.max_input_bytes
        || width == 0
        || height == 0
        || width > MAX_DIMENSION
        || height > MAX_DIMENSION
    {
        return Err(error("thumbnail input or output exceeds supported limits"));
    }
    // Give each format probe a fresh reader with the same cancellation boundary
    let reader = || {
        BufReader::new(CancelReader {
            input: Cursor::new(data),
            cancel,
        })
    };
    // Reject oversized decoded dimensions before allocating source pixels
    let valid = |w: u32, h: u32| {
        if w == 0 || h == 0 || u64::from(w) * u64::from(h) > bounds.max_source_pixels {
            Err(error("invalid thumbnail source dimensions"))
        } else {
            Ok(())
        }
    };
    let (sw, sh, fw, fh, pixels) = if data.starts_with(b"GIF87a") || data.starts_with(b"GIF89a") {
        let mut options = gif::DecodeOptions::new();
        options.set_color_output(gif::ColorOutput::RGBA);
        options.set_memory_limit(gif::MemoryLimit::Bytes(
            std::num::NonZeroU64::new((bounds.max_source_pixels * 32).max(8 << 20)).unwrap(),
        ));
        let mut decoder = options.read_info(reader())?;
        let sw = u32::from(decoder.width());
        let sh = u32::from(decoder.height());
        valid(sw, sh)?;
        let frame = decoder
            .read_next_frame()?
            .ok_or_else(|| error("missing GIF frame"))?;
        let fw = u32::from(frame.width);
        let fh = u32::from(frame.height);
        valid(fw, fh)?;
        if u32::from(frame.left) + fw > sw || u32::from(frame.top) + fh > sh {
            return Err(error("invalid GIF frame bounds"));
        }
        (sw, sh, fw, fh, frame.buffer.to_vec())
    } else {
        let format = image::guess_format(data)?;
        if !matches!(format, image::ImageFormat::Jpeg | image::ImageFormat::Png) {
            return Err(error("unsupported thumbnail format"));
        }
        let (sw, sh) = image::ImageReader::with_format(reader(), format).into_dimensions()?;
        valid(sw, sh)?;
        let mut decoder = image::ImageReader::with_format(reader(), format);
        let mut limits = image::Limits::default();
        limits.max_alloc = Some((bounds.max_source_pixels * 32).max(8 << 20));
        limits.max_image_width = Some(sw);
        limits.max_image_height = Some(sh);
        decoder.limits(limits);
        let source = decoder.decode()?.to_rgba8();
        (sw, sh, sw, sh, source.into_raw())
    };
    check()?;
    if fit_within {
        (width, height) = fit_with_limits(sw, sh, width, height, bounds)?;
    }
    let mut output = vec![0; width as usize * height as usize * 4];
    for y in 0..height {
        check()?;
        let sy = u64::from(y) * u64::from(sh) / u64::from(height);
        for x in 0..width {
            let sx = u64::from(x) * u64::from(sw) / u64::from(width);
            if sx >= u64::from(fw) || sy >= u64::from(fh) {
                continue;
            }
            let from = ((sy * u64::from(fw) + sx) * 4) as usize;
            let to = ((y * width + x) * 4) as usize;
            output[to..to + 4].copy_from_slice(&pixels[from..from + 4]);
        }
    }
    check()?;
    Ok(Thumbnail {
        width,
        height,
        data: output,
    })
}

// Fit the image into the requested display size
pub fn fit(width: u32, height: u32, max_width: u32, max_height: u32) -> Result<(u32, u32)> {
    fit_with_limits(width, height, max_width, max_height, Limits::default())
}
// Fit an image with explicit resource limits
pub fn fit_with_limits(
    width: u32,
    height: u32,
    max_width: u32,
    max_height: u32,
    bounds: Limits,
) -> Result<(u32, u32)> {
    bounds.validate()?;
    if width == 0
        || height == 0
        || u64::from(width) * u64::from(height) > bounds.max_source_pixels
        || max_width == 0
        || max_height == 0
        || max_width > MAX_DIMENSION
        || max_height > MAX_DIMENSION
    {
        return Err(error("invalid thumbnail fit dimensions"));
    }
    if width <= max_width && height <= max_height {
        return Ok((width, height));
    }
    if u64::from(max_width) * u64::from(height) <= u64::from(max_height) * u64::from(width) {
        Ok((
            max_width,
            ((u64::from(height) * u64::from(max_width) / u64::from(width)) as u32).max(1),
        ))
    } else {
        Ok((
            ((u64::from(width) * u64::from(max_height) / u64::from(height)) as u32).max(1),
            max_height,
        ))
    }
}
// Decode the supplied data under its validation policy
pub fn decode(data: &[u8], width: u32, height: u32, cancel: &AtomicBool) -> Result<Thumbnail> {
    decode_with_limits(data, width, height, cancel, Limits::default())
}
// Decode and fit the image in one bounded operation
pub fn fit_decode(
    data: &[u8],
    max_width: u32,
    max_height: u32,
    cancel: &AtomicBool,
) -> Result<Thumbnail> {
    fit_decode_with_limits(data, max_width, max_height, cancel, Limits::default())
}
// Decode image data with explicit resource limits
pub fn decode_with_limits(
    data: &[u8],
    width: u32,
    height: u32,
    cancel: &AtomicBool,
    bounds: Limits,
) -> Result<Thumbnail> {
    decode_image(data, width, height, cancel, false, bounds)
}
// Decode and fit image data with explicit resource limits
pub fn fit_decode_with_limits(
    data: &[u8],
    width: u32,
    height: u32,
    cancel: &AtomicBool,
    bounds: Limits,
) -> Result<Thumbnail> {
    decode_image(data, width, height, cancel, true, bounds)
}
// Encode the image as a PNG payload
pub fn png(source: &Thumbnail, cancel: &AtomicBool) -> Result<Vec<u8>> {
    use image::ImageEncoder;
    if cancel.load(Ordering::Acquire) {
        return Err(error("thumbnail cancelled"));
    }
    if source.width == 0
        || source.height == 0
        || source.width > MAX_DIMENSION
        || source.height > MAX_DIMENSION
        || source.data.len() != source.width as usize * source.height as usize * 4
    {
        return Err(error("invalid thumbnail pixels"));
    }
    let mut output = Vec::new();
    image::codecs::png::PngEncoder::new(&mut output).write_image(
        &source.data,
        source.width,
        source.height,
        image::ExtendedColorType::Rgba8,
    )?;
    if cancel.load(Ordering::Acquire) {
        return Err(error("thumbnail cancelled"));
    }
    Ok(output)
}
// Wrap the encoded image in a PNG data URI
pub fn data_uri(source: &Thumbnail, cancel: &AtomicBool) -> Result<String> {
    use base64::Engine;
    Ok(format!(
        "data:image/png;base64,{}",
        base64::engine::general_purpose::STANDARD.encode(png(source, cancel)?)
    ))
}
pub struct Rendered {
    pub width: u32,
    pub height: u32,
    pub data_uri: String,
}
// Produce the current display representation for bounded thumbnail decoding
pub fn render(
    data: &[u8],
    max_width: u32,
    max_height: u32,
    cancel: &AtomicBool,
) -> Result<Rendered> {
    render_with_limits(data, max_width, max_height, cancel, Limits::default())
}
// Render the thumbnail with explicit resource limits
pub fn render_with_limits(
    data: &[u8],
    max_width: u32,
    max_height: u32,
    cancel: &AtomicBool,
    bounds: Limits,
) -> Result<Rendered> {
    let source = fit_decode_with_limits(data, max_width, max_height, cancel, bounds)?;
    Ok(Rendered {
        width: source.width,
        height: source.height,
        data_uri: data_uri(&source, cancel)?,
    })
}
